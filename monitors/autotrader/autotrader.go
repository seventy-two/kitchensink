// Package autotrader watches an AutoTrader UK search and notifies newly
// listed cars.
package autotrader

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("autotrader", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const (
	gatewayURL = "https://www.autotrader.co.uk/at-gateway?opname=SearchResultsListingsGridQuery"
	siteURL    = "https://www.autotrader.co.uk"
	userAgent  = "Mozilla/5.0 (X11; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0"
	// baselineKey records that the first check has run. Adverts found by
	// the first check are logged quietly, since they aren't new.
	baselineKey = "baselined"
)

// listingsQuery asks for only the fields the monitor needs.
const listingsQuery = `query SearchResultsListingsGridQuery($filters: [FilterInput!]!, $channel: Channel!, $page: Int, $sortBy: SearchResultsSort, $listingType: [ListingType!], $searchId: String!, $featureFlags: [FeatureFlag]) {
  searchResults(input: {facets: [], filters: $filters, channel: $channel, page: $page, sortBy: $sortBy, listingType: $listingType, searchId: $searchId, featureFlags: $featureFlags}) {
    listings { ... on SearchListing { type advertId title subTitle attentionGrabber price vehicleLocation sellerType images badges { type displayText } } }
  }
}`

// Config is the monitor's config.
type Config struct {
	// SearchURL is a search results page URL copied from the site, e.g.
	// https://www.autotrader.co.uk/car-search?make=Mazda&model=Mazda3&postcode=...
	SearchURL string `json:"searchURL"`
	// GatewayURL replaces AutoTrader's API endpoint, for tests.
	GatewayURL string `json:"gatewayURL"`
}

// Monitor is the AutoTrader monitor.
type Monitor struct {
	search  *search
	gateway string
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.SearchURL == "" {
		return nil, fmt.Errorf("autotrader needs a searchURL")
	}
	s, err := parseSearch(c.SearchURL)
	if err != nil {
		return nil, err
	}
	m := &Monitor{search: s, gateway: c.GatewayURL}
	if m.gateway == "" {
		m.gateway = gatewayURL
	}
	return m, nil
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	listings, err := m.fetch(ctx, env.HTTP)
	if err != nil {
		// If AutoTrader starts blocking us, the next poll just tries again.
		return monitor.Result{}, err
	}
	var baselined bool
	if _, err := env.State.Get(baselineKey, &baselined); err != nil {
		return monitor.Result{}, err
	}

	res := monitor.Result{Blocks: []monitor.Block{m.list(listings)}, Summary: summary(listings), Subtitle: m.search.describe(), Timely: true}
	for _, l := range listings {
		res.Events = append(res.Events, monitor.Event{
			Key:      "advert:" + l.AdvertID,
			Title:    strings.TrimSpace(fmt.Sprintf("New %s · %s", m.search.name(), l.Price)),
			Body:     l.summary(),
			URL:      l.url(),
			Image:    l.image(),
			Priority: monitor.PriorityHigh,
			Quiet:    !baselined,
		})
	}
	if !baselined {
		env.Log.Info("watching search", "name", m.search.name(), "listed", len(listings))
		if err := env.State.Set(baselineKey, true); err != nil {
			return monitor.Result{}, err
		}
	}
	return res, nil
}

type listing struct {
	Type             string   `json:"type"`
	AdvertID         string   `json:"advertId"`
	Title            string   `json:"title"`
	SubTitle         string   `json:"subTitle"`
	AttentionGrabber string   `json:"attentionGrabber"`
	Price            string   `json:"price"`
	VehicleLocation  string   `json:"vehicleLocation"`
	SellerType       string   `json:"sellerType"`
	Images           []string `json:"images"`
	Badges           []struct {
		Type        string `json:"type"`
		DisplayText string `json:"displayText"`
	} `json:"badges"`
}

type graphQLResponse struct {
	Data struct {
		SearchResults struct {
			Listings []listing `json:"listings"`
		} `json:"searchResults"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// fetch returns the first page of the search's results, newest first. Only
// genuine results count: promoted adverts and suggestions don't necessarily
// match the search.
func (m *Monitor) fetch(ctx context.Context, client *http.Client) ([]listing, error) {
	body, err := json.Marshal([]map[string]any{{
		"operationName": "SearchResultsListingsGridQuery",
		"query":         listingsQuery,
		"variables": map[string]any{
			"filters":      m.search.filters,
			"channel":      m.search.channel,
			"page":         1,
			"sortBy":       "most_recent",
			"listingType":  nil,
			"searchId":     newSearchID(),
			"featureFlags": []string{},
		},
	}})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.gateway, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Origin", siteURL)
	req.Header.Set("Referer", siteURL+"/car-search")
	req.Header.Set("x-sauron-app-name", "sauron-search-results-app")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned %s", resp.Status)
	}

	var results []graphQLResponse
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, fmt.Errorf("decoding search results: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("empty search response")
	}
	if len(results[0].Errors) > 0 {
		return nil, fmt.Errorf("search failed: %s", results[0].Errors[0].Message)
	}

	var listings []listing
	for _, l := range results[0].Data.SearchResults.Listings {
		if l.Type == "NATURAL_LISTING" && l.AdvertID != "" {
			listings = append(listings, l)
		}
	}
	return listings, nil
}

func (m *Monitor) list(listings []listing) monitor.Block {
	b := monitor.Block{
		Type:  monitor.BlockList,
		Title: fmt.Sprintf("%d listed", len(listings)),
		Items: []monitor.Item{},
	}
	for _, l := range listings {
		it := monitor.Item{
			Title:    strings.TrimSpace(l.Title + " · " + l.Price),
			Subtitle: l.summary(),
			Detail:   l.SubTitle,
			URL:      l.url(),
			Image:    l.image(),
		}
		if l.writeOff() != "" {
			it.Tone = monitor.ToneWarn
		}
		b.Items = append(b.Items, it)
	}
	return b
}

// summary is how many are listed, the newest and the cheapest.
func summary(listings []listing) []monitor.Stat {
	out := []monitor.Stat{{Label: "Listed", Value: strconv.Itoa(len(listings))}}
	if len(listings) == 0 {
		return out
	}
	newest := listings[0]
	out = append(out, monitor.Stat{Label: "Newest", Value: newest.Price, Detail: newest.brief()})
	cheapest, best := -1, 0
	for i, l := range listings {
		if p, ok := price(l.Price); ok && (cheapest < 0 || p < best) {
			cheapest, best = i, p
		}
	}
	if cheapest > 0 {
		l := listings[cheapest]
		out = append(out, monitor.Stat{Label: "Cheapest", Value: l.Price, Detail: l.brief()})
	}
	return out
}

// brief is the year and mileage, e.g. "2022 (22 reg) · 14,000 miles".
func (l *listing) brief() string {
	var parts []string
	for _, b := range l.Badges {
		switch b.Type {
		case "REGISTERED_YEAR", "MILEAGE":
			parts = append(parts, b.DisplayText)
		}
	}
	return strings.Join(parts, " · ")
}

// price reads a price such as "£13,871" in whole pounds.
func price(s string) (int, bool) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, strings.SplitN(s, ".", 2)[0])
	n, err := strconv.Atoi(digits)
	return n, err == nil
}

func (l *listing) url() string { return siteURL + "/car-details/" + l.AdvertID }

func (l *listing) image() string {
	if len(l.Images) == 0 {
		return ""
	}
	return strings.Replace(l.Images[0], "{resize}", "w800h600", 1)
}

// summary is a one-line description: year, mileage, location, seller and
// anything worth a warning.
func (l *listing) summary() string {
	var parts []string
	add := func(s string) {
		if s != "" {
			parts = append(parts, s)
		}
	}
	add(l.brief())
	add(l.VehicleLocation)
	add(sellerName(l.SellerType))
	if w := l.writeOff(); w != "" {
		add(w)
	}
	return strings.Join(parts, " · ")
}

func (l *listing) writeOff() string {
	for _, b := range l.Badges {
		if b.Type == "WRITE_OFF_CATEGORY" {
			return b.DisplayText + " write-off"
		}
	}
	return ""
}

func sellerName(t string) string {
	switch t {
	case "PRIVATE":
		return "Private"
	case "TRADE":
		return "Dealer"
	default:
		return ""
	}
}

// newSearchID makes a random UUID, as the site sends with each search.
func newSearchID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
