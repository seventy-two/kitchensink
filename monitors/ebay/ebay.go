// Package ebay watches an eBay search for new listings, through eBay's
// Browse API (the site itself turns away anything that isn't a browser),
// and names what each listing is with Gemini, as listing titles are padded
// with keywords.
package ebay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("ebay", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// SearchURL is a search page copied from the site, e.g.
	// https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=0; its
	// keywords (_nkw) and category (_sacat) are searched for. Or give Query
	// and Category instead.
	SearchURL string `json:"searchURL"`
	Query     string `json:"query"`
	Category  string `json:"category"`
	// Marketplace is eBay's site, e.g. EBAY_GB (the default) or EBAY_US.
	Marketplace string `json:"marketplace"`
	// ClientID and ClientSecret are a Production keyset from
	// developer.ebay.com (the App ID and Cert ID); use "${NAME}" to keep
	// them in the environment.
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	// Name says what's being listed, for Gemini, e.g. "a boxed PC game".
	// Listings are shown by what Gemini calls them, or by their titles
	// without Gemini.
	Name   string `json:"name"`
	Gemini Gemini `json:"gemini"`
	// Notify is the least rarity Gemini must give a new listing for it to
	// notify: "uncommon" or "rare". Others make no event, so they're only
	// in the panel. Empty notifies for every new listing, as does one
	// Gemini gave no rarity.
	Notify string `json:"notify"`
	// Limit is how many of the newest listings to read and show (default
	// 50, at most 200).
	Limit int `json:"limit"`
	// APIURL replaces https://api.ebay.com, for tests.
	APIURL string `json:"apiURL"`
	// Timezone is for listing times; it defaults to Europe/London.
	Timezone string `json:"timezone"`
}

// Monitor is the eBay monitor.
type Monitor struct {
	cfg    Config
	loc    *time.Location
	gemini *gemini

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.SearchURL != "" {
		u, err := url.Parse(c.SearchURL)
		if err != nil {
			return nil, fmt.Errorf("ebay searchURL: %w", err)
		}
		if c.Query == "" {
			c.Query = u.Query().Get("_nkw")
		}
		if c.Category == "" && u.Query().Get("_sacat") != "0" {
			c.Category = u.Query().Get("_sacat")
		}
	}
	if c.Query == "" {
		return nil, fmt.Errorf("ebay needs a searchURL or a query")
	}
	if c.ClientID == "" || c.ClientSecret == "" {
		return nil, fmt.Errorf("ebay needs a clientId and clientSecret (a Production keyset from developer.ebay.com)")
	}
	if c.Marketplace == "" {
		c.Marketplace = "EBAY_GB"
	}
	if c.Notify != "" && rank(c.Notify) < 0 {
		return nil, fmt.Errorf("ebay notify must be one of %s", strings.Join(rarities, ", "))
	}
	if c.Limit <= 0 {
		c.Limit = 50
	}
	c.Limit = min(c.Limit, 200)
	if c.APIURL == "" {
		c.APIURL = "https://api.ebay.com"
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
		m.gemini = newGemini(c.Gemini)
	}
	return m, nil
}

// item is one listing, as the Browse API gives it.
type item struct {
	ItemID          string   `json:"itemId"`
	Title           string   `json:"title"`
	Price           *amount  `json:"price"`
	CurrentBidPrice *amount  `json:"currentBidPrice"`
	BuyingOptions   []string `json:"buyingOptions"`
	BidCount        int      `json:"bidCount"`
	ItemWebURL      string   `json:"itemWebUrl"`
	Image           struct {
		ImageURL string `json:"imageUrl"`
	} `json:"image"`
	ItemCreationDate time.Time `json:"itemCreationDate"`
}

type amount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

// auction is true for a listing that only takes bids.
func (it item) auction() bool {
	for _, o := range it.BuyingOptions {
		if o == "FIXED_PRICE" {
			return false
		}
	}
	for _, o := range it.BuyingOptions {
		if o == "AUCTION" {
			return true
		}
	}
	return false
}

// price is what it costs now: the current bid for an auction.
func (it item) price() string {
	a := it.Price
	if it.auction() && it.CurrentBidPrice != nil {
		a = it.CurrentBidPrice
	}
	if a == nil {
		return ""
	}
	v, err := strconv.ParseFloat(a.Value, 64)
	if err != nil {
		return a.Value + " " + a.Currency
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	s = strings.TrimSuffix(s, ".00")
	switch a.Currency {
	case "GBP":
		return "£" + s
	case "USD":
		return "$" + s
	case "EUR":
		return "€" + s
	}
	return s + " " + a.Currency
}

// kind says how it's sold, for the price's detail line.
func (it item) kind() string {
	if !it.auction() {
		return ""
	}
	switch it.BidCount {
	case 0:
		return "auction"
	case 1:
		return "1 bid"
	}
	return fmt.Sprintf("%d bids", it.BidCount)
}

// link is the listing's page without eBay's tracking parameters.
func (it item) link() string {
	if u, err := url.Parse(it.ItemWebURL); err == nil {
		u.RawQuery = ""
		return u.String()
	}
	return it.ItemWebURL
}

// accessToken returns an application token, fetching a new one when the
// last has expired (they last two hours).
func (m *Monitor) accessToken(ctx context.Context, client *http.Client, now time.Time) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token != "" && now.Before(m.expires) {
		return m.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {"https://api.ebay.com/oauth/api_scope"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.APIURL+"/identity/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(m.cfg.ClientID, m.cfg.ClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := do(client, req, &out); err != nil {
		return "", fmt.Errorf("eBay sign-in: %w", err)
	}
	m.token = out.AccessToken
	// A minute's margin, so a token isn't used as it runs out.
	m.expires = now.Add(time.Duration(out.ExpiresIn)*time.Second - time.Minute)
	return m.token, nil
}

// search returns the newest listings.
func (m *Monitor) search(ctx context.Context, client *http.Client, now time.Time) ([]item, error) {
	token, err := m.accessToken(ctx, client, now)
	if err != nil {
		return nil, err
	}
	q := url.Values{"q": {m.cfg.Query}, "sort": {"newlyListed"}, "limit": {strconv.Itoa(m.cfg.Limit)}}
	if m.cfg.Category != "" {
		q.Set("category_ids", m.cfg.Category)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.APIURL+"/buy/browse/v1/item_summary/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-EBAY-C-MARKETPLACE-ID", m.cfg.Marketplace)
	var out struct {
		ItemSummaries []item `json:"itemSummaries"`
	}
	if err := do(client, req, &out); err != nil {
		if strings.Contains(err.Error(), "401") {
			m.mu.Lock()
			m.token = ""
			m.mu.Unlock()
		}
		return nil, fmt.Errorf("eBay search: %w", err)
	}
	return out.ItemSummaries, nil
}

func do(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// State keys.
const (
	baselineKey = "baselined" // the first check has run; its listings weren't new
	seenKey     = "seen"      // listings already reported, by ID, with when
	namesKey    = "names"     // what Gemini called each listing, by ID
	rarityKey   = "rarity"    // how rare Gemini said each listing is, by ID
	ignoredKey  = "ignored"   // games never to flag, by key(name), with their names
)

// forget is how long a listing is remembered after it was last in the
// results, so one that drops out and back isn't reported again.
const forget = 60 * 24 * time.Hour

// summaryRows is how many of the newest listings the short view shows.
const summaryRows = 3

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	items, err := m.search(ctx, env.HTTP, now)
	if err != nil {
		return monitor.Result{}, err
	}

	names, rarity := map[string]string{}, map[string]string{}
	if _, err := env.State.Get(namesKey, &names); err != nil {
		return monitor.Result{}, err
	}
	if _, err := env.State.Get(rarityKey, &rarity); err != nil {
		return monitor.Result{}, err
	}
	ignored := map[string]string{}
	if _, err := env.State.Get(ignoredKey, &ignored); err != nil {
		return monitor.Result{}, err
	}
	// Listings named before rarity was asked for are asked about again.
	var unjudged []item
	for _, it := range items {
		if _, ok := rarity[it.ItemID]; !ok {
			unjudged = append(unjudged, it)
		}
	}
	judgedAll := true
	if m.gemini != nil && len(unjudged) > 0 {
		got, err := m.gemini.judge(ctx, env.HTTP, m.cfg.Name, unjudged)
		if err != nil {
			// Shown by title for now; tried again next poll.
			env.Log.Warn("naming listings", "err", err)
			judgedAll = false
		}
		for id, j := range got {
			names[id], rarity[id] = j.Name, j.Rarity
		}
	}
	nameOf := func(it item) string {
		if n := names[it.ItemID]; n != "" {
			return n
		}
		return strings.TrimSpace(it.Title)
	}
	// An ignored game counts as common, whatever Gemini says.
	rarityOf := func(it item) string {
		if _, ok := ignored[key(nameOf(it))]; ok {
			return "common"
		}
		return rarity[it.ItemID]
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
	for _, it := range items {
		if _, ok := seen[it.ItemID]; !ok {
			// Wait for Gemini's say before reporting it, unless there's
			// no Gemini to ask.
			if _, ok := rarity[it.ItemID]; m.gemini != nil && !ok && !judgedAll {
				continue
			}
			// Only listings worth a notification become events: the
			// first check's, and those below notify, would only fill
			// Recent.
			if r := rarityOf(it); baselined && m.notifies(r) {
				body := it.price()
				if k := it.kind(); k != "" {
					body += " · " + k
				}
				if rank(r) > 0 {
					body += " · " + r
				}
				events = append(events, monitor.Event{
					Key:   "listing/" + it.ItemID,
					Time:  it.ItemCreationDate,
					Title: nameOf(it),
					Body:  body,
					URL:   it.link(),
					Image: it.Image.ImageURL,
				})
			}
		}
		seen[it.ItemID] = now
	}
	for id, t := range seen {
		if now.Sub(t) > forget {
			delete(seen, id)
			delete(names, id)
			delete(rarity, id)
		}
	}
	for _, kv := range []struct {
		key string
		v   any
	}{{seenKey, seen}, {namesKey, names}, {rarityKey, rarity}, {baselineKey, true}} {
		if err := env.State.Set(kv.key, kv.v); err != nil {
			return monitor.Result{}, err
		}
	}

	table := m.table(items, nameOf, rarityOf, now)
	short := m.table(items[:min(summaryRows, len(items))], nameOf, rarityOf, now)
	var summary []monitor.Stat
	for _, it := range items[:min(summaryRows, len(items))] {
		summary = append(summary, monitor.Stat{Label: nameOf(it), Value: it.price(), Detail: m.when(it.ItemCreationDate, now)})
	}
	return monitor.Result{
		Blocks:       []monitor.Block{table},
		Summary:      summary,
		SummaryBlock: &short,
		Events:       events,
		Timely:       true,
	}, nil
}

// notifies says whether a new listing of this rarity is worth a
// notification.
func (m *Monitor) notifies(rarity string) bool {
	r := rank(rarity)
	return m.cfg.Notify == "" || r < 0 || r >= rank(m.cfg.Notify)
}

// table lists the listings; uncommon and rare ones say so under their name,
// rare ones stand out, and both have a button to ignore the game.
func (m *Monitor) table(items []item, nameOf, rarityOf func(item) string, now time.Time) monitor.Block {
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
	for _, it := range items {
		name := monitor.Cell{Text: nameOf(it), URL: it.link()}
		r := rarityOf(it)
		var ignore monitor.Cell
		if rank(r) > 0 {
			name.Detail = r
			ignore.Action = &monitor.CellAction{Type: ignoreAction, ID: it.ItemID, Label: "Ignore " + nameOf(it) + " from now on"}
		}
		if r == "rare" {
			name.Tone = monitor.ToneAccent
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			name,
			{Text: it.price(), Detail: it.kind()},
			{Text: m.when(it.ItemCreationDate, now), Tone: monitor.ToneMuted},
			ignore,
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

// ignoreAction is the action that ignores a listing's game: from then on,
// its listings count as common, so they're neither marked nor notified.
const ignoreAction = "ignore"

// key is how game names are matched when ignoring: Gemini may write the
// same name with different capitals or punctuation.
func key(name string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		default:
			space = true
		}
	}
	return b.String()
}

// Act ignores a listing's game (an action of type "ignore" with the
// listing's item ID).
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != ignoreAction {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
	}
	names := map[string]string{}
	if _, err := env.State.Get(namesKey, &names); err != nil {
		return err
	}
	name := names[a.ID]
	if name == "" {
		return fmt.Errorf("%w: no listing %q with a name", monitor.ErrBadAction, a.ID)
	}
	ignored := map[string]string{}
	if _, err := env.State.Get(ignoredKey, &ignored); err != nil {
		return err
	}
	ignored[key(name)] = name
	env.Log.Info("ignoring a game", "name", name)
	return env.State.Set(ignoredKey, ignored)
}
