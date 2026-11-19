// Package stocks watches a list of symbols using CNBC's quote API. It
// notifies when a symbol's daily move passes each multiple of a percentage,
// and when a price crosses a target.
package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("stocks", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const defaultQuoteURL = "https://quote.cnbc.com/quote-html-webservice/restQuote/symbolType/symbol"

// Config is the monitor's config.
type Config struct {
	// Symbols are CNBC symbols, e.g. "AAPL", ".FTSE", "BTC.CM=".
	Symbols []string `json:"symbols"`
	// Holdings are the symbols you hold, largest first. They lead the
	// table in this order, highlighted, and make up the summary in place
	// of the biggest movers. Any not in Symbols are watched too.
	Holdings []string `json:"holdings"`
	// MovePct notifies each time a symbol's daily move passes another
	// multiple of it, so 5 notifies at 5%, 10%, 15%... either way, while
	// its market is open. With Holdings set, only holdings notify. 0 turns
	// move alerts off. It defaults to 5.
	MovePct *float64 `json:"movePct"`
	// Targets notify when a symbol's price crosses above or below a level.
	Targets map[string]Target `json:"targets"`
	// Portfolio adds an estimate of an account's value and the day's gain
	// or loss, from the same quotes. It's marked private for clients.
	Portfolio *Portfolio `json:"portfolio"`
	// QuoteURL and ChartURL replace CNBC's endpoints, for tests.
	QuoteURL string `json:"quoteURL"`
	ChartURL string `json:"chartURL"`
}

// Target is a price level to watch for.
type Target struct {
	Above *float64 `json:"above"`
	Below *float64 `json:"below"`
}

// Monitor is the stocks monitor.
type Monitor struct {
	cfg     Config
	extra   map[string]bool // upper case: symbols fetched only for the portfolio
	movePct float64
	rank    map[string]int // holding symbol, upper case, to its place
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	rank := map[string]int{}
	for i, h := range c.Holdings {
		h = strings.ToUpper(strings.TrimSpace(h))
		if _, dup := rank[h]; dup {
			continue
		}
		rank[h] = i
		if !slices.ContainsFunc(c.Symbols, func(s string) bool { return strings.EqualFold(s, h) }) {
			c.Symbols = append(c.Symbols, h)
		}
	}
	if len(c.Symbols) == 0 {
		return nil, fmt.Errorf("stocks needs at least one symbol")
	}
	if c.QuoteURL == "" {
		c.QuoteURL = defaultQuoteURL
	}
	if c.ChartURL == "" {
		c.ChartURL = defaultChartURL
	}
	m := &Monitor{cfg: c, movePct: 5, rank: rank, extra: map[string]bool{}}
	if c.Portfolio != nil {
		for _, s := range c.Portfolio.symbols() {
			if !slices.ContainsFunc(c.Symbols, func(o string) bool { return strings.EqualFold(o, s) }) {
				m.extra[strings.ToUpper(s)] = true
			}
		}
	}
	if c.MovePct != nil {
		m.movePct = *c.MovePct
	}
	return m, nil
}

// quote is the part of a CNBC quote the monitor uses.
type quote struct {
	Symbol       string `json:"symbol"`
	Code         int    `json:"code"`
	Name         string `json:"name"`
	Last         string `json:"last"`
	LastTime     string `json:"last_time"`
	Change       string `json:"change"`
	ChangePct    string `json:"change_pct"`
	CurrencyCode string `json:"currencyCode"`
	PrevClose    string `json:"previous_day_closing"`
	MarketStatus string `json:"curmktstatus"`
	Extended     *struct {
		Type      string `json:"type"`
		Last      string `json:"last"`
		Change    string `json:"change"`
		ChangePct string `json:"change_pct"`
	} `json:"ExtendedMktQuote"`
}

type quoteResponse struct {
	FormattedQuoteResult struct {
		FormattedQuote []quote `json:"FormattedQuote"`
	} `json:"FormattedQuoteResult"`
}

func (m *Monitor) fetch(ctx context.Context, client *http.Client) ([]quote, error) {
	q := url.Values{
		"symbols":       {strings.Join(m.fetched(), "|")},
		"requestMethod": {"itv"},
		"noform":        {"1"},
		"partnerId":     {"2"},
		"fund":          {"1"},
		"exthrs":        {"1"},
		"output":        {"json"},
		"events":        {"1"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.QuoteURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quotes returned %s", resp.Status)
	}
	var qr quoteResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, fmt.Errorf("decoding quotes: %w", err)
	}
	var out []quote
	for _, q := range qr.FormattedQuoteResult.FormattedQuote {
		if q.Code == 0 && q.Name != "" {
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no quotes found for %v", m.cfg.Symbols)
	}
	return out, nil
}

// crossedKey is the state key recording which targets each symbol was last
// past, so a target notifies once per crossing.
const crossedKey = "crossed"

// fetched is every symbol to quote: the watchlist, then any more the
// portfolio needs.
func (m *Monitor) fetched() []string {
	out := slices.Clone(m.cfg.Symbols)
	for s := range m.extra {
		out = append(out, s)
	}
	slices.Sort(out[len(m.cfg.Symbols):])
	return out
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	all, err := m.fetch(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}
	all = newest(env, all)
	var quotes []quote
	for _, q := range all {
		if got, want, odd := oddMove(q); odd {
			env.Log.Warn("quote's move disagrees with its prices", "symbol", q.Symbol, "change_pct", got, "from_prices", want,
				"last", q.Last, "prev_close", q.PrevClose, "last_time", q.LastTime, "status", q.MarketStatus)
		}
		if !m.extra[strings.ToUpper(q.Symbol)] {
			quotes = append(quotes, q)
		}
	}

	crossed := map[string]bool{}
	if _, err := env.State.Get(crossedKey, &crossed); err != nil {
		return monitor.Result{}, err
	}
	moves := map[string]notedMove{}
	if _, err := env.State.Get(movesKey, &moves); err != nil {
		return monitor.Result{}, err
	}
	var events []monitor.Event
	for _, q := range quotes {
		news := m.belowNoted(q, m.moveEvents(q, env.Now()), moves)
		events = append(events, news...)
		events = append(events, m.moveUpdates(q, env.Now(), news, moves)...)
		events = append(events, m.targetEvents(q, crossed, env.Now().Unix())...)
	}
	if err := env.State.Set(crossedKey, crossed); err != nil {
		return monitor.Result{}, err
	}
	// Only today's moves are kept up to date.
	today := env.Now().In(london).Format(time.DateOnly)
	maps.DeleteFunc(moves, func(_ string, n notedMove) bool { return n.Day != today })
	if err := env.State.Set(movesKey, moves); err != nil {
		return monitor.Result{}, err
	}

	var symbols []string
	for _, q := range quotes {
		symbols = append(symbols, q.Symbol)
	}
	weekAgo := m.weekAgo(ctx, env, symbols)
	sparks := m.sparks(ctx, env, quotes)
	res := monitor.Result{
		Blocks:       []monitor.Block{m.chartsBlock(quotes, sparks, m.histories(ctx, env, quotes), weekAgo, env.Now())},
		Summary:      m.summary(quotes, sparks),
		SummaryStyle: monitor.SummaryChips,
		Events:       events,
		Timely:       true,
	}
	if p := m.cfg.Portfolio; p != nil {
		bySymbol := map[string]quote{}
		for _, q := range all {
			bySymbol[strings.ToUpper(q.Symbol)] = q
		}
		now := env.Now()
		e := p.estimate(bySymbol, now)
		var days []day
		if _, err := env.State.Get(daysKey, &days); err != nil {
			return monitor.Result{}, err
		}
		days = record(days, e, now)
		if err := env.State.Set(daysKey, days); err != nil {
			return monitor.Result{}, err
		}
		res.Blocks = append(portfolioBlocks(e, days, now), res.Blocks...)
		res.Summary = append([]monitor.Stat{portfolioStat(e)}, res.Summary...)
	}
	return res, nil
}

// summary is every quote, as the table orders them, or without holdings
// the biggest movers today, either way, first. Clients scroll a line of
// chips that doesn't fit. Each chip has its day's price line from sparks,
// if there is one, and outside the session leads with the pre-market or
// after-hours move, tagged.
func (m *Monitor) summary(quotes []quote, sparks map[string]chipSpark) []monitor.Stat {
	var picked []quote
	if len(m.rank) > 0 {
		picked = m.ordered(quotes)
	} else {
		picked = append([]quote(nil), quotes...)
		sort.SliceStable(picked, func(i, j int) bool {
			a, _ := parsePct(picked[i].ChangePct)
			b, _ := parsePct(picked[j].ChangePct)
			return math.Abs(a) > math.Abs(b)
		})
	}
	var out []monitor.Stat
	for _, q := range picked {
		out = append(out, m.chip(q, sparks[q.Symbol]))
	}
	return out
}

// chip is a quote's day: its move with the day's line, or outside the
// session the pre-market or after-hours move, tagged.
func (m *Monitor) chip(q quote, cs chipSpark) monitor.Stat {
	st := monitor.Stat{Label: displayName(q), Value: formatPct(q.ChangePct), Tone: toneOf(q.ChangePct), Spark: cs.spark}
	if v, tag, ok := extMove(q, cs); ok {
		st.Value, st.Tag, st.Tone = v, tag, toneOf(q.Extended.ChangePct)
	}
	return st
}

func (m *Monitor) held(q quote) bool {
	_, ok := m.rank[strings.ToUpper(q.Symbol)]
	return ok
}

// moveEvents notifies for each multiple of movePct the day's move has
// passed. Keys include the trading day, so each level fires once a day.
// Only today's regular session counts: before the open CNBC still shows
// the last session's move, and early each morning it has sent an old
// day's quotes marked as in session, so the quote must be from today too.
// With holdings set, other symbols are only watched.
func (m *Monitor) moveEvents(q quote, now time.Time) []monitor.Event {
	pct, ok := parsePct(q.ChangePct)
	if !ok || m.movePct <= 0 || q.MarketStatus != "REG_MKT" || !tradedToday(q, now) || len(m.rank) > 0 && !m.held(q) {
		return nil
	}
	steps := int(math.Abs(pct) / m.movePct)
	if steps == 0 {
		return nil
	}
	// Only the highest level is worth a notification; lower ones may
	// already have fired, and if not they're stale.
	level := float64(steps) * m.movePct
	dir := "up"
	if pct < 0 {
		dir = "down"
	}
	prio := monitor.PriorityNormal
	if steps >= 2 {
		prio = monitor.PriorityHigh
	}
	day := q.LastTime
	if len(day) > 10 {
		day = day[:10]
	}
	return []monitor.Event{{
		Key:      fmt.Sprintf("move:%s:%s:%s%g", q.Symbol, day, dir, level),
		Title:    fmt.Sprintf("%s %s %.2f%% today", displayName(q), dir, math.Abs(pct)),
		Body:     fmt.Sprintf("%s %s (%s)", q.Last, q.CurrencyCode, q.Change),
		URL:      "https://www.cnbc.com/quotes/" + url.PathEscape(q.Symbol),
		Priority: prio,
	}}
}

// belowNoted leaves out q's new move event if a higher level's already
// been noted today the same way: it's shown until the move's back under
// the first level.
func (m *Monitor) belowNoted(q quote, news []monitor.Event, moves map[string]notedMove) []monitor.Event {
	pct, ok := parsePct(q.ChangePct)
	if !ok || len(news) == 0 || len(q.LastTime) < 10 {
		return news
	}
	level := float64(int(math.Abs(pct)/m.movePct)) * m.movePct
	for _, n := range moves {
		if n.Symbol == q.Symbol && n.Day == q.LastTime[:10] && n.Up == (pct > 0) && n.Level > level {
			return nil
		}
	}
	return news
}

// movesKey is the state key holding the day's notified moves, by event
// key, so their events can be kept up to date.
const movesKey = "moves"

// notedMove is a move that's been notified: the symbol, its trading day,
// which way and past what level, and the furthest it's been that way.
type notedMove struct {
	Symbol string  `json:"symbol"`
	Day    string  `json:"day"`
	Up     bool    `json:"up"`
	Level  float64 `json:"level"`
	Peak   float64 `json:"peak"`
	// Ended is set while it's fallen back under its level, its event
	// ended so clients drop it.
	Ended bool `json:"ended,omitempty"`
}

// revived is how long a move's event shows again once it's back past its
// level, as long as clients keep a new one.
const revived = 24 * time.Hour

// moveUpdates keeps the day's move events for q up to date as its price
// changes: while the move's still past its level the event shows the
// latest figures; once it's fallen back under the first level (movePct)
// the event is ended (until now), so clients drop it, and if it passes
// that again it shows again. A move that's passed a higher level
// since replaces its lower one: that event is ended (until now) and
// forgotten, so Recent shows one per symbol and direction. news are the
// move events q raised this poll; ones not seen before are noted, and need
// no update yet.
func (m *Monitor) moveUpdates(q quote, now time.Time, news []monitor.Event, moves map[string]notedMove) []monitor.Event {
	pct, ok := parsePct(q.ChangePct)
	if !ok || !tradedToday(q, now) {
		return nil
	}
	day := q.LastTime[:10]
	fresh := map[string]bool{}
	for _, e := range news {
		if _, seen := moves[e.Key]; !seen {
			fresh[e.Key] = true
			level := float64(int(math.Abs(pct)/m.movePct)) * m.movePct
			moves[e.Key] = notedMove{Symbol: q.Symbol, Day: day, Up: pct > 0, Level: level, Peak: math.Abs(pct)}
		}
	}
	dir := "up"
	if pct < 0 {
		dir = "down"
	}
	latest := fmt.Sprintf("%s %s (%s)", q.Last, q.CurrencyCode, q.Change)
	// The highest level noted each way.
	top := map[bool]float64{}
	for _, n := range moves {
		if n.Symbol == q.Symbol && n.Day == day {
			top[n.Up] = math.Max(top[n.Up], n.Level)
		}
	}
	var out []monitor.Event
	for key, n := range moves {
		if n.Symbol != q.Symbol || n.Day != day {
			continue
		}
		if n.Level < top[n.Up] {
			delete(moves, key)
			out = append(out, monitor.Event{Key: key, Update: true, Title: fmt.Sprintf("%s %s %.2f%% today", displayName(q), dir, math.Abs(pct)),
				Body: latest, Until: now})
			continue
		}
		// How far it's gone in the noted direction.
		along := pct
		if !n.Up {
			along = -pct
		}
		n.Peak = math.Max(n.Peak, along)
		moves[key] = n
		if fresh[key] {
			continue // first raised just now: it says this already
		}
		ev := monitor.Event{Key: key, Update: true, Title: fmt.Sprintf("%s %s %.2f%% today", displayName(q), dir, math.Abs(pct)), Body: latest}
		// Only under the first level is it over: one that passed 6% and
		// slipped to 5% is still a move worth seeing.
		under := along < m.movePct
		switch {
		case under && n.Ended:
			continue // gone already
		case under:
			ev.Until, n.Ended = now, true
		case n.Ended:
			ev.Until, n.Ended = now.Add(revived), false
		}
		moves[key] = n
		out = append(out, ev)
	}
	slices.SortFunc(out, func(a, b monitor.Event) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// targetEvents notifies when a price moves past a target it wasn't past
// at the last check.
func (m *Monitor) targetEvents(q quote, crossed map[string]bool, now int64) []monitor.Event {
	t, ok := m.cfg.Targets[q.Symbol]
	if !ok {
		return nil
	}
	price, ok := parseNumber(q.Last)
	if !ok {
		return nil
	}
	var events []monitor.Event
	check := func(dir string, level *float64, past bool) {
		if level == nil {
			return
		}
		key := fmt.Sprintf("%s:%s:%g", q.Symbol, dir, *level)
		if past && !crossed[key] {
			events = append(events, monitor.Event{
				Key:      fmt.Sprintf("target:%s:%d", key, now),
				Title:    fmt.Sprintf("%s is %s %g", displayName(q), dir, *level),
				Body:     fmt.Sprintf("%s %s (%s today)", q.Last, q.CurrencyCode, formatPct(q.ChangePct)),
				URL:      "https://www.cnbc.com/quotes/" + url.PathEscape(q.Symbol),
				Priority: monitor.PriorityHigh,
			})
		}
		crossed[key] = past
	}
	if t.Above != nil {
		check("above", t.Above, price > *t.Above)
	}
	if t.Below != nil {
		check("below", t.Below, price < *t.Below)
	}
	return events
}

// ordered is your holdings, largest first, then the other quotes, biggest
// risers first.
func (m *Monitor) ordered(quotes []quote) []quote {
	sorted := append([]quote(nil), quotes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		hi, hj := m.held(sorted[i]), m.held(sorted[j])
		switch {
		case hi && hj:
			return m.rank[strings.ToUpper(sorted[i].Symbol)] < m.rank[strings.ToUpper(sorted[j].Symbol)]
		case hi != hj:
			return hi
		}
		a, _ := parsePct(sorted[i].ChangePct)
		b, _ := parsePct(sorted[j].ChangePct)
		return a > b
	})
	return sorted
}

func displayName(q quote) string {
	if strings.HasPrefix(q.Symbol, ".") || strings.Contains(q.Symbol, "=") {
		return q.Name
	}
	return q.Symbol
}

func toneOf(pct string) monitor.Tone {
	v, ok := parsePct(pct)
	switch {
	case !ok || v == 0:
		return monitor.ToneNone
	case v > 0:
		return monitor.ToneGood
	default:
		return monitor.ToneBad
	}
}

// formatPct rounds a CNBC percentage to two places, since small caps
// come with as many as four ("+5.4756%").
func formatPct(s string) string {
	pct, ok := parsePct(s)
	if !ok || !strings.ContainsAny(s, "0123456789") {
		return s
	}
	return fmt.Sprintf("%+.2f%%", pct)
}

// parsePct parses "+1.06%"; CNBC writes no change as "UNCH".
// oddMove reports a quote whose move (got) is more than a point away from
// the one its last price and previous close give (want). CNBC sent one
// around the open on 19 Nov 2026 (NVDA -2.89% between two readings of
// about +4%); this logs any more, to find which field was wrong.
func oddMove(q quote) (got, want float64, odd bool) {
	got, ok1 := parsePct(q.ChangePct)
	last, ok2 := parseNumber(q.Last)
	prev, ok3 := parseNumber(q.PrevClose)
	if !ok1 || !ok2 || !ok3 || prev <= 0 {
		return 0, 0, false
	}
	want = (last - prev) / prev * 100
	return got, want, math.Abs(got-want) > 1
}

func parsePct(s string) (float64, bool) {
	if s == "UNCH" {
		return 0, true
	}
	return parseNumber(strings.TrimSuffix(s, "%"))
}

// parseNumber parses CNBC's formatted numbers, e.g. "10,552.05".
func parseNumber(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	return v, err == nil
}
