// Package stocks watches a list of symbols using CNBC's quote API. It
// notifies when a symbol's daily move passes each multiple of a percentage,
// and when a price crosses a target.
package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

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
	m := &Monitor{cfg: c, movePct: 5, rank: rank}
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
		"symbols":       {strings.Join(m.cfg.Symbols, "|")},
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

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	quotes, err := m.fetch(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}

	crossed := map[string]bool{}
	if _, err := env.State.Get(crossedKey, &crossed); err != nil {
		return monitor.Result{}, err
	}
	var events []monitor.Event
	for _, q := range quotes {
		events = append(events, m.moveEvents(q)...)
		events = append(events, m.targetEvents(q, crossed, env.Now().Unix())...)
	}
	if err := env.State.Set(crossedKey, crossed); err != nil {
		return monitor.Result{}, err
	}

	var symbols []string
	for _, q := range quotes {
		symbols = append(symbols, q.Symbol)
	}
	weekAgo := m.weekAgo(ctx, env, symbols)
	return monitor.Result{
		Blocks:       []monitor.Block{m.table(quotes, weekAgo)},
		Summary:      m.summary(quotes),
		SummaryStyle: monitor.SummaryChips,
		Events:       events,
	}, nil
}

// moversLen is how many symbols the summary shows.
const moversLen = 4

// summary is your holdings, largest first, or without any the biggest
// movers today, either way.
func (m *Monitor) summary(quotes []quote) []monitor.Stat {
	var picked []quote
	if len(m.rank) > 0 {
		for _, q := range quotes {
			if m.held(q) {
				picked = append(picked, q)
			}
		}
		sort.SliceStable(picked, func(i, j int) bool {
			return m.rank[strings.ToUpper(picked[i].Symbol)] < m.rank[strings.ToUpper(picked[j].Symbol)]
		})
	} else {
		picked = append([]quote(nil), quotes...)
		sort.SliceStable(picked, func(i, j int) bool {
			a, _ := parsePct(picked[i].ChangePct)
			b, _ := parsePct(picked[j].ChangePct)
			return math.Abs(a) > math.Abs(b)
		})
		picked = picked[:min(moversLen, len(picked))]
	}
	var out []monitor.Stat
	for _, q := range picked {
		out = append(out, monitor.Stat{Label: displayName(q), Value: formatPct(q.ChangePct), Tone: toneOf(q.ChangePct)})
	}
	return out
}

func (m *Monitor) held(q quote) bool {
	_, ok := m.rank[strings.ToUpper(q.Symbol)]
	return ok
}

// moveEvents notifies for each multiple of movePct the day's move has
// passed. Keys include the trading day, so each level fires once a day.
// Only the regular session counts: before the open CNBC still shows the
// last session's move, and its day can come back wrong, which once
// notified Friday's moves again on Monday morning. With holdings set,
// other symbols are only watched.
func (m *Monitor) moveEvents(q quote) []monitor.Event {
	pct, ok := parsePct(q.ChangePct)
	if !ok || m.movePct <= 0 || q.MarketStatus != "REG_MKT" || len(m.rank) > 0 && !m.held(q) {
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

// table lists your holdings, largest first, then the other quotes,
// biggest risers first. weekAgo holds closes from a week before, by
// symbol, for the 1w column.
func (m *Monitor) table(quotes []quote, weekAgo map[string]float64) monitor.Block {
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
	b := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Symbol"},
			{Title: "Name"},
			{Title: "Last", Align: monitor.AlignRight},
			{Title: "Change", Align: monitor.AlignRight},
			{Title: "%", Align: monitor.AlignRight},
			{Title: "Extended", Align: monitor.AlignRight},
			{Title: "1w", Align: monitor.AlignRight},
		},
		Rows: [][]monitor.Cell{},
	}
	for _, q := range sorted {
		tone := toneOf(q.ChangePct)
		ext := monitor.Cell{Tone: monitor.ToneMuted}
		if x := q.Extended; x != nil && x.Last != "" && q.MarketStatus != "REG_MKT" {
			ext = monitor.Cell{Text: x.Last + " " + formatPct(x.ChangePct), Tone: toneOf(x.ChangePct)}
		}
		symbol := monitor.Cell{Text: q.Symbol, URL: "https://www.cnbc.com/quotes/" + url.PathEscape(q.Symbol)}
		if m.held(q) {
			symbol.Tone = monitor.ToneAccent
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			symbol,
			{Text: q.Name, Tone: monitor.ToneMuted},
			{Text: q.Last},
			{Text: q.Change, Tone: tone},
			{Text: formatPct(q.ChangePct), Tone: tone},
			ext,
			weekCell(q, weekAgo),
		})
	}
	return b
}

// weekCell is the change since a week ago, blank when that's unknown.
func weekCell(q quote, weekAgo map[string]float64) monitor.Cell {
	ref, ok := weekAgo[q.Symbol]
	last, okLast := parseNumber(q.Last)
	if !ok || !okLast || ref <= 0 {
		return monitor.Cell{}
	}
	pct := fmt.Sprintf("%+.2f%%", (last/ref-1)*100)
	return monitor.Cell{Text: pct, Tone: toneOf(pct)}
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
