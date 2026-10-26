// Package reverb follows Reverb searches for new listings, through Reverb's
// API, and has Gemini check each one is what's wanted, as searches turn up
// parts, cases and lookalike models along with the real thing.
package reverb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("reverb", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// Searches are Reverb searches, e.g. "ibanez xiphos"; their listings
	// are followed together, newest first.
	Searches []string `json:"searches"`
	// Token is a Reverb personal access token (reverb.com, My Account,
	// API & Integrations); use "${NAME}" to keep it in the environment.
	Token string `json:"token"`
	// Want says what's being looked for, for Gemini: which models count
	// and which don't. Without Gemini, every listing is shown.
	Want   string `json:"want"`
	Gemini Gemini `json:"gemini"`
	// ShipsTo is a country code: only listings that ship there are found,
	// and their postage there is shown. Default GB; "any" for everywhere.
	ShipsTo string `json:"shipsTo"`
	// Currency prices are shown in (default GBP).
	Currency string `json:"currency"`
	// Limit is how many of each search's newest listings to read (default
	// 50, at most 100).
	Limit int `json:"limit"`
	// APIURL replaces https://api.reverb.com, for tests.
	APIURL string `json:"apiURL"`
	// Timezone is for listing times; it defaults to Europe/London.
	Timezone string `json:"timezone"`
}

// Monitor is the Reverb monitor.
type Monitor struct {
	cfg    Config
	loc    *time.Location
	gemini *gemini
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Searches) == 0 {
		return nil, fmt.Errorf("reverb needs searches")
	}
	if c.Token == "" {
		return nil, fmt.Errorf("reverb needs a token (a personal access token from reverb.com)")
	}
	switch strings.ToLower(c.ShipsTo) {
	case "":
		c.ShipsTo = "GB"
	case "any":
		c.ShipsTo = ""
	}
	if c.Currency == "" {
		c.Currency = "GBP"
	}
	if c.Limit <= 0 {
		c.Limit = 50
	}
	c.Limit = min(c.Limit, 100)
	if c.APIURL == "" {
		c.APIURL = "https://api.reverb.com"
	}
	c.APIURL = strings.TrimRight(c.APIURL, "/")
	if c.Timezone == "" {
		c.Timezone = "Europe/London"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	m := &Monitor{cfg: c, loc: loc}
	if c.Gemini.APIKey != "" {
		if c.Want == "" {
			return nil, fmt.Errorf("reverb needs want, saying what's wanted, for Gemini")
		}
		m.gemini = newGemini(c.Gemini)
	}
	return m, nil
}

// listing is one listing, as the API gives it.
type listing struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Make        string `json:"make"`
	Model       string `json:"model"`
	Year        string `json:"year"`
	Finish      string `json:"finish"`
	Description string `json:"description"`
	Condition   struct {
		DisplayName string `json:"display_name"`
	} `json:"condition"`
	Categories []struct {
		FullName string `json:"full_name"`
	} `json:"categories"`
	Price    money `json:"price"`
	Shipping *struct {
		UserRegionRate *struct {
			Rate money `json:"rate"`
		} `json:"user_region_rate"`
	} `json:"shipping"`
	PublishedAt time.Time `json:"published_at"`
	Photos      []struct {
		Links struct {
			LargeCrop link `json:"large_crop"`
		} `json:"_links"`
	} `json:"photos"`
	Links struct {
		Web link `json:"web"`
	} `json:"_links"`
}

type money struct {
	Display string `json:"display"`
}

type link struct {
	Href string `json:"href"`
}

func (l listing) id() string { return strconv.FormatInt(l.ID, 10) }

// postage is what it costs to send to ShipsTo, if known.
func (l listing) postage() string {
	if l.Shipping == nil || l.Shipping.UserRegionRate == nil {
		return ""
	}
	switch d := l.Shipping.UserRegionRate.Rate.Display; d {
	case "":
		return ""
	case "FREE":
		return "free postage"
	default:
		return "+ " + d + " postage"
	}
}

func (l listing) image() string {
	if len(l.Photos) == 0 {
		return ""
	}
	return l.Photos[0].Links.LargeCrop.Href
}

// search returns a search's newest listings.
func (m *Monitor) search(ctx context.Context, client *http.Client, query string) ([]listing, error) {
	q := url.Values{"query": {query}, "sort": {"published_at|desc"}, "per_page": {strconv.Itoa(m.cfg.Limit)}}
	if m.cfg.ShipsTo != "" {
		q.Set("ships_to", m.cfg.ShipsTo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.APIURL+"/api/listings?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.Token)
	req.Header.Set("Accept-Version", "3.0")
	req.Header.Set("Accept", "application/hal+json")
	req.Header.Set("X-Display-Currency", m.cfg.Currency)
	if m.cfg.ShipsTo != "" {
		req.Header.Set("X-Shipping-Region", m.cfg.ShipsTo)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("Reverb search %q: %s: %s", query, resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Listings []listing `json:"listings"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("Reverb search %q: %w", query, err)
	}
	return out.Listings, nil
}

// listings runs every search and returns their listings together, once
// each, newest first.
func (m *Monitor) listings(ctx context.Context, client *http.Client) ([]listing, error) {
	var all []listing
	have := map[int64]bool{}
	for _, s := range m.cfg.Searches {
		got, err := m.search(ctx, client, s)
		if err != nil {
			return nil, err
		}
		for _, l := range got {
			if !have[l.ID] {
				have[l.ID] = true
				all = append(all, l)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].PublishedAt.After(all[j].PublishedAt) })
	return all, nil
}

// State keys.
const (
	baselineKey = "baselined" // the first check has run; its listings weren't new
	seenKey     = "seen"      // listings already reported, by ID, with when
	verdictsKey = "verdicts"  // what Gemini said of each listing, by ID
	rejectedKey = "rejected"  // listings turned down by hand, by ID, with their names
)

// forget is how long a listing is remembered after it was last in the
// results, so one that drops out and back isn't reported again.
const forget = 60 * 24 * time.Hour

// summaryRows is how many of the newest listings the short view shows.
const summaryRows = 3

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	all, err := m.listings(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}

	verdicts := map[string]verdict{}
	if _, err := env.State.Get(verdictsKey, &verdicts); err != nil {
		return monitor.Result{}, err
	}
	rejected := map[string]string{}
	if _, err := env.State.Get(rejectedKey, &rejected); err != nil {
		return monitor.Result{}, err
	}
	if m.gemini != nil {
		var unjudged []listing
		for _, l := range all {
			if _, ok := verdicts[l.id()]; !ok {
				unjudged = append(unjudged, l)
			}
		}
		for len(unjudged) > 0 {
			n := min(len(unjudged), geminiBatch)
			got, err := m.gemini.judge(ctx, env.HTTP, m.cfg.Want, unjudged[:n])
			if err != nil {
				// Left out for now; asked about again next poll.
				env.Log.Warn("checking listings", "err", err)
				break
			}
			for id, v := range got {
				verdicts[id] = v
			}
			unjudged = unjudged[n:]
		}
	}
	// wanted is the listings to show: those Gemini says match, or all of
	// them without Gemini, less any turned down by hand.
	var wanted []listing
	for _, l := range all {
		if _, ok := rejected[l.id()]; ok {
			continue
		}
		if v, ok := verdicts[l.id()]; m.gemini == nil || ok && v.Match {
			wanted = append(wanted, l)
		}
	}
	nameOf := func(l listing) string {
		if n := verdicts[l.id()].Name; n != "" {
			return n
		}
		return strings.TrimSpace(l.Title)
	}

	var seen map[string]time.Time
	if _, err := env.State.Get(seenKey, &seen); err != nil {
		return monitor.Result{}, err
	}
	if seen == nil {
		seen = map[string]time.Time{}
	}
	var baselined bool
	if _, err := env.State.Get(baselineKey, &baselined); err != nil {
		return monitor.Result{}, err
	}
	var events []monitor.Event
	for _, l := range wanted {
		if _, ok := seen[l.id()]; !ok && baselined {
			body := l.Price.Display
			if p := l.postage(); p != "" {
				body += " " + p
			}
			if n := verdicts[l.id()].Note; n != "" {
				body += " · " + n
			}
			events = append(events, monitor.Event{
				Key:   "listing/" + l.id(),
				Time:  l.PublishedAt,
				Title: nameOf(l),
				Body:  body,
				URL:   l.Links.Web.Href,
				Image: l.image(),
			})
		}
	}
	// Only listings that have been judged count as seen, so one waiting on
	// Gemini is still new once it's found to match.
	for _, l := range all {
		if _, ok := verdicts[l.id()]; m.gemini == nil || ok {
			seen[l.id()] = now
		}
	}
	for id, t := range seen {
		if now.Sub(t) > forget {
			delete(seen, id)
			delete(verdicts, id)
			delete(rejected, id)
		}
	}
	for _, kv := range []struct {
		key string
		v   any
	}{{seenKey, seen}, {verdictsKey, verdicts}, {rejectedKey, rejected}, {baselineKey, true}} {
		if err := env.State.Set(kv.key, kv.v); err != nil {
			return monitor.Result{}, err
		}
	}

	table := m.table(wanted, nameOf, verdicts, now)
	shortList := wanted[:min(summaryRows, len(wanted))]
	short := m.table(shortList, nameOf, verdicts, now)
	var summary []monitor.Stat
	for _, l := range shortList {
		summary = append(summary, monitor.Stat{Label: nameOf(l), Value: l.Price.Display, Detail: m.when(l.PublishedAt, now)})
	}
	return monitor.Result{
		Blocks:       []monitor.Block{table},
		Summary:      summary,
		SummaryBlock: &short,
		Events:       events,
	}, nil
}

// table lists the listings, each with a button to turn it down.
func (m *Monitor) table(ls []listing, nameOf func(listing) string, verdicts map[string]verdict, now time.Time) monitor.Block {
	b := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Listing", Fill: true},
			{Title: "Price", Align: monitor.AlignRight},
			{Title: "Listed", Align: monitor.AlignRight},
			{Title: ""},
		},
		Rows: [][]monitor.Cell{},
	}
	for _, l := range ls {
		detail := l.Condition.DisplayName
		if n := verdicts[l.id()].Note; n != "" {
			detail = strings.TrimPrefix(detail+" · "+n, " · ")
		}
		var reject monitor.Cell
		if m.gemini != nil {
			reject.Action = &monitor.CellAction{Type: rejectAction, ID: l.id(), Label: "Not what I'm after"}
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			{Text: nameOf(l), URL: l.Links.Web.Href, Detail: detail},
			{Text: l.Price.Display, Detail: l.postage()},
			{Text: m.when(l.PublishedAt, now), Tone: monitor.ToneMuted},
			reject,
		})
	}
	return b
}

// when says when a listing went up: the time today, the day this week,
// else the date.
func (m *Monitor) when(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	t, now = t.In(m.loc), now.In(m.loc)
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return t.Format("15:04")
	case now.Sub(t) < 6*24*time.Hour:
		return t.Format("Mon 15:04")
	case y1 == y2:
		return t.Format("2 Jan")
	}
	return t.Format("2 Jan 2006")
}

// rejectAction turns down a listing Gemini let through, by its ID: it's no
// longer shown.
const rejectAction = "reject"

// Act turns down a listing (an action of type "reject" with its ID).
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != rejectAction {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
	}
	verdicts := map[string]verdict{}
	if _, err := env.State.Get(verdictsKey, &verdicts); err != nil {
		return err
	}
	v, ok := verdicts[a.ID]
	if !ok {
		return fmt.Errorf("%w: no listing %q", monitor.ErrBadAction, a.ID)
	}
	rejected := map[string]string{}
	if _, err := env.State.Get(rejectedKey, &rejected); err != nil {
		return err
	}
	rejected[a.ID] = v.Name
	env.Log.Info("turning down a listing", "id", a.ID, "name", v.Name)
	return env.State.Set(rejectedKey, rejected)
}
