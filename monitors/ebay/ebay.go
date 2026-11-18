// Package ebay watches an eBay search for new listings, through eBay's
// Browse API (the site itself turns away anything that isn't a browser),
// and names what each listing is with Gemini, as listing titles are padded
// with keywords.
package ebay

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
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
	// Grails are games always worth having, a tier above rare
	// ("grail"), whatever the model says: matched against its name for a
	// listing (any game in a lot), ignoring capitals and punctuation. An
	// entry ending in "*" matches names starting with it, e.g. "Metal Gear
	// Solid*" for the whole series.
	Grails []string `json:"grails"`
	// Deals judges the asking price (postage included) of Buy It Now
	// listings in pounds that are uncommon, rare or grail: a good deal,
	// fair or overpriced against what such things sell for, asked of the
	// model with web search, once a price.
	Deals bool `json:"deals"`
	// Prices are fair prices you know, in pounds with postage, by game
	// name as for Grails (one game, not lots). They settle any Buy It Now
	// listing of the game without asking the model: under 80% of the
	// price is a good deal, over 125% overpriced.
	Prices map[string]float64 `json:"prices"`
	// Notify is the least rarity Gemini must give a new listing for it to
	// notify: "uncommon", "rare" or "grail". Others make no event, so they're only
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
		return nil, fmt.Errorf("ebay notify must be one of %s", strings.Join(tiers, ", "))
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
	ShippingOptions  []struct {
		ShippingCost *amount `json:"shippingCost"`
	} `json:"shippingOptions"`
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
	return money(a)
}

// postage is the cheapest delivery eBay quotes, if it gave one.
func (it item) postage() *amount {
	var best *amount
	bestV := 0.0
	for _, o := range it.ShippingOptions {
		if o.ShippingCost == nil {
			continue
		}
		v, err := strconv.ParseFloat(o.ShippingCost.Value, 64)
		if err == nil && (best == nil || v < bestV) {
			best, bestV = o.ShippingCost, v
		}
	}
	return best
}

// highPostage is true when postage is over half a Buy It Now price. An
// auction's final price isn't known, so its postage isn't judged.
func (it item) highPostage() bool {
	p := it.postage()
	if it.auction() || it.Price == nil || p == nil || p.Currency != it.Price.Currency {
		return false
	}
	price, err1 := strconv.ParseFloat(it.Price.Value, 64)
	post, err2 := strconv.ParseFloat(p.Value, 64)
	return err1 == nil && err2 == nil && post > price/2
}

// postageText is the postage for the price's detail line: empty when it's
// free or not given.
func (it item) postageText() string {
	p := it.postage()
	if v, err := strconv.ParseFloat(p.valueOr("0"), 64); p == nil || err != nil || v == 0 {
		return ""
	}
	return "+" + money(p) + " postage"
}

func (a *amount) valueOr(def string) string {
	if a == nil {
		return def
	}
	return a.Value
}

// money writes an amount, e.g. "£3.50" or "£20".
func money(a *amount) string {
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
	clearKey    = "clear"     // games just ignored, by key(name): their listings' events to end
	hiddenKey   = "hidden"    // listings hidden by hand (duds), by ID, with when
	unshownKey  = "unshown"   // listings just hidden, by ID: their events to end
	dealsKey    = "deals"     // what each listing's asking price is, by ID
)

// maxDeals is how many listings' prices the model is asked about in one
// poll; the rest wait for the next.
const maxDeals = 16

// forget is how long a listing is remembered after it was last in the
// results, so one that drops out and back isn't reported again.
const forget = 60 * 24 * time.Hour

// summaryRows is how many of the newest uncommon or rare listings the
// short view shows, from those listed within shortFor.
const (
	summaryRows = 3
	shortFor    = 24 * time.Hour
)

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
	// An ignored game counts as common, and a grail is one, whatever the
	// model says.
	rarityOf := func(it item) string {
		if _, ok := ignored[key(nameOf(it))]; ok {
			return "common"
		}
		if m.grail(nameOf(it)) {
			return grail
		}
		return rarity[it.ItemID]
	}

	hidden := map[string]time.Time{}
	if _, err := env.State.Get(hiddenKey, &hidden); err != nil {
		return monitor.Result{}, err
	}
	items = slices.DeleteFunc(items, func(it item) bool { _, ok := hidden[it.ItemID]; return ok })
	deals := map[string]deal{}
	if _, err := env.State.Get(dealsKey, &deals); err != nil {
		return monitor.Result{}, err
	}
	m.judgeDeals(ctx, env, items, nameOf, rarityOf, deals)

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
				if p := it.postageText(); p != "" {
					body += " " + p
				}
				if it.highPostage() {
					body += " (high)"
				}
				if k := it.kind(); k != "" {
					body += " · " + k
				}
				if rank(r) > 0 {
					body += " · " + r
				}
				if d, ok := deals[it.ItemID]; ok && d.Verdict != "fair" {
					body += " · " + dealLabel(d.Verdict)
				}
				priority := monitor.PriorityNormal
				if r == grail {
					priority = monitor.PriorityHigh
				}
				events = append(events, monitor.Event{
					Priority: priority,
					Key:      "listing/" + it.ItemID,
					Time:     it.ItemCreationDate,
					Title:    nameOf(it),
					Body:     body,
					URL:      it.link(),
					Image:    it.Image.ImageURL,
				})
			}
		}
		seen[it.ItemID] = now
	}
	// A game just ignored: its listings' notifications end, so clients
	// drop them. Ones that never notified are no event at all, and the
	// engine passes over them.
	clear := map[string]bool{}
	if _, err := env.State.Get(clearKey, &clear); err != nil {
		return monitor.Result{}, err
	}
	if len(clear) > 0 {
		var ids []string
		for id, name := range names {
			if clear[key(name)] {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for _, id := range ids {
			events = append(events, monitor.Event{Key: "listing/" + id, Update: true, Title: names[id], Body: "Ignored", Until: now})
		}
		if err := env.State.Set(clearKey, map[string]bool{}); err != nil {
			return monitor.Result{}, err
		}
	}
	// Listings just hidden: their notifications end too.
	unshown := map[string]bool{}
	if _, err := env.State.Get(unshownKey, &unshown); err != nil {
		return monitor.Result{}, err
	}
	if len(unshown) > 0 {
		for _, id := range slices.Sorted(maps.Keys(unshown)) {
			events = append(events, monitor.Event{Key: "listing/" + id, Update: true, Title: cmp.Or(names[id], "Listing"), Body: "Hidden", Until: now})
		}
		if err := env.State.Set(unshownKey, map[string]bool{}); err != nil {
			return monitor.Result{}, err
		}
	}
	for id, t := range seen {
		if now.Sub(t) > forget {
			delete(seen, id)
			delete(names, id)
			delete(rarity, id)
			delete(deals, id)
		}
	}
	for id, t := range hidden {
		if now.Sub(t) > forget {
			delete(hidden, id)
		}
	}
	for _, kv := range []struct {
		key string
		v   any
	}{{seenKey, seen}, {namesKey, names}, {rarityKey, rarity}, {baselineKey, true}, {dealsKey, deals}, {hiddenKey, hidden}} {
		if err := env.State.Set(kv.key, kv.v); err != nil {
			return monitor.Result{}, err
		}
	}

	table := m.table(items, nameOf, rarityOf, deals, now)
	// Short, only the newest uncommon and rare listings from the last
	// day: nothing at all when there are none.
	var worth []item
	for _, it := range items {
		if rank(rarityOf(it)) > 0 && now.Sub(it.ItemCreationDate) < shortFor && len(worth) < summaryRows {
			worth = append(worth, it)
		}
	}
	short := m.table(worth, nameOf, rarityOf, deals, now)
	var summary []monitor.Stat
	for _, it := range worth {
		summary = append(summary, monitor.Stat{Label: nameOf(it), Value: it.price(), Detail: m.when(it.ItemCreationDate, now)})
	}
	return monitor.Result{
		Blocks:       []monitor.Block{table},
		Summary:      summary,
		SummaryBlock: &short,
		// Nothing worth seeing today: off the dashboard until there is.
		Background: len(worth) == 0,
		Events:     events,
		Timely:     true,
	}, nil
}

// notifies says whether a new listing of this rarity is worth a
// notification.
func (m *Monitor) notifies(rarity string) bool {
	r := rank(rarity)
	return m.cfg.Notify == "" || r < 0 || r >= rank(m.cfg.Notify)
}

// table lists the listings; uncommon, rare and grail ones say so under
// their name, rare and grail ones stand out, and all have a button to
// ignore the game. A price with postage over half of it is marked.
func (m *Monitor) table(items []item, nameOf, rarityOf func(item) string, deals map[string]deal, now time.Time) monitor.Block {
	b := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Listing", Fill: true},
			{Title: "Price", Align: monitor.AlignRight},
			{Title: "Deal"},
			{Title: "Listed", Align: monitor.AlignRight},
			{Title: ""},
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
		switch r {
		case grail:
			name.Tone = monitor.ToneGood
		case "rare":
			name.Tone = monitor.ToneAccent
		}
		price := monitor.Cell{Text: it.price(), Detail: it.kind()}
		if p := it.postageText(); p != "" {
			price.Detail = strings.TrimPrefix(price.Detail+" · "+p, " · ")
		}
		if it.highPostage() {
			// Postage over half the price: worth knowing before buying.
			price.Tone = monitor.ToneWarn
			price.Detail = strings.Replace(price.Detail, "postage", "postage, over half", 1)
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			name,
			price,
			dealCell(deals[it.ItemID]),
			{Text: m.when(it.ItemCreationDate, now), Tone: monitor.ToneMuted},
			{Action: &monitor.CellAction{Type: hideAction, ID: it.ItemID, Label: "Hide this listing (a dud); keep looking for the game", Icon: "delete"}},
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

// dealCell shows a listing's deal: the verdict, and the fair range (or
// your price) under it.
func dealCell(d deal) monitor.Cell {
	if d.Verdict == "" {
		return monitor.Cell{}
	}
	c := monitor.Cell{Text: dealLabel(d.Verdict), Detail: fmt.Sprintf("£%.0f–%.0f", d.Low, d.High)}
	if d.Yours {
		c.Detail = fmt.Sprintf("yours £%.0f", d.Low/yourLow)
	}
	switch d.Verdict {
	case "good":
		c.Tone = monitor.ToneGood
	case "overpriced":
		c.Tone = monitor.ToneWarn
	default:
		c.Tone = monitor.ToneMuted
	}
	return c
}

// dealLabel is a verdict as people say it.
func dealLabel(v string) string {
	if v == "good" {
		return "good deal"
	}
	return v
}

// asking is what a Buy It Now listing in pounds costs with postage; false
// for auctions and other currencies.
func (it item) asking() (float64, bool) {
	if it.auction() || it.Price == nil || it.Price.Currency != "GBP" {
		return 0, false
	}
	v, err := strconv.ParseFloat(it.Price.Value, 64)
	if err != nil {
		return 0, false
	}
	if p := it.postage(); p != nil {
		if p.Currency != "GBP" {
			return 0, false
		}
		post, err := strconv.ParseFloat(p.Value, 64)
		if err != nil {
			return 0, false
		}
		v += post
	}
	return v, true
}

// yourLow and yourHigh make a price you know into a fair range.
const (
	yourLow  = 0.8
	yourHigh = 1.25
)

// yourDeal judges asking against a price you know.
func yourDeal(asking, price float64) deal {
	d := deal{Verdict: "fair", Low: price * yourLow, High: price * yourHigh, Asking: asking, Yours: true}
	switch {
	case asking < d.Low:
		d.Verdict = "good"
	case asking > d.High:
		d.Verdict = "overpriced"
	}
	return d
}

// judgeDeals fills deals for listings that need one: Buy It Now in pounds,
// at a price not yet judged; your price for a game in Prices, else (with
// Deals) the model's view for uncommon, rare and grail listings. A failure
// is logged and tried again next poll.
func (m *Monitor) judgeDeals(ctx context.Context, env monitor.Env, items []item, nameOf, rarityOf func(item) string, deals map[string]deal) {
	asking := map[string]float64{}
	var ask []item
	for _, it := range items {
		total, ok := it.asking()
		if !ok {
			continue
		}
		if d, ok := deals[it.ItemID]; ok && math.Abs(d.Asking-total) < 0.005 {
			continue
		}
		if price, ok := m.yourPrice(nameOf(it)); ok {
			deals[it.ItemID] = yourDeal(total, price)
			continue
		}
		if m.cfg.Deals && m.gemini != nil && rank(rarityOf(it)) > 0 && len(ask) < maxDeals {
			asking[it.ItemID] = total
			ask = append(ask, it)
		}
	}
	for i := 0; i < len(ask); i += dealBatch {
		got, err := m.gemini.deals(ctx, env.HTTP, m.cfg.Name, ask[i:min(i+dealBatch, len(ask))], asking)
		if err != nil {
			env.Log.Warn("judging prices", "err", err)
			return
		}
		maps.Copy(deals, got)
	}
}

// yourPrice is the config's price for a game named name, if it has one.
func (m *Monitor) yourPrice(name string) (float64, bool) {
	if strings.Contains(name, " + ") {
		return 0, false
	}
	k := key(name)
	for g, price := range m.cfg.Prices {
		if nameMatches(g, k) {
			return price, true
		}
	}
	return 0, false
}

// nameMatches says whether a game name's key k is the one pattern names:
// equal ignoring capitals and punctuation, or starting with it when it
// ends in "*".
func nameMatches(pattern, k string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		p := key(prefix)
		return k == p || strings.HasPrefix(k, p+" ")
	}
	return k == key(pattern)
}

// hideAction hides one listing (a dud) for good, without ignoring its game.
const hideAction = "hide"

// ignoreAction is the action that ignores a listing's game: from then on,
// its listings count as common, so they're neither marked nor notified.
const ignoreAction = "ignore"

// grail says whether a listing named name is one of Grails, or a lot with
// one in it.
func (m *Monitor) grail(name string) bool {
	for _, part := range strings.Split(name, " + ") {
		for _, g := range m.cfg.Grails {
			if nameMatches(g, key(part)) {
				return true
			}
		}
	}
	return false
}

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
	if a.Type == hideAction {
		return m.hide(env, a.ID)
	}
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
	if err := env.State.Set(ignoredKey, ignored); err != nil {
		return err
	}
	// The poll after this ends the game's notifications.
	clear := map[string]bool{}
	if _, err := env.State.Get(clearKey, &clear); err != nil {
		return err
	}
	clear[key(name)] = true
	return env.State.Set(clearKey, clear)
}

// hide hides one listing for good: it leaves the panel and its
// notification ends, but its game is still looked for.
func (m *Monitor) hide(env monitor.Env, id string) error {
	if id == "" {
		return fmt.Errorf("%w: hide needs a listing ID", monitor.ErrBadAction)
	}
	hidden := map[string]time.Time{}
	if _, err := env.State.Get(hiddenKey, &hidden); err != nil {
		return err
	}
	hidden[id] = env.Now()
	if err := env.State.Set(hiddenKey, hidden); err != nil {
		return err
	}
	unshown := map[string]bool{}
	if _, err := env.State.Get(unshownKey, &unshown); err != nil {
		return err
	}
	unshown[id] = true
	return env.State.Set(unshownKey, unshown)
}
