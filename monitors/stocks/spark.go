package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// A chip's spark is the day's price line from CNBC's intraday chart: the
// regular session, then pre-market or after-hours trading as Ext. Charts
// are fetched at most every sparkEvery per symbol, and only once its quote
// has moved; between fetches the live price is added to the cached line.

// sparkKey is the state key holding each symbol's cached spark.
const sparkKey = "spark"

// sparkEvery is how often a symbol's chart may be fetched again.
const sparkEvery = 5 * time.Minute

// linePoints and extPoints cap the points sent for the session and for a
// whole extended session; a partial extended session gets its share.
const (
	linePoints = 40
	extPoints  = 12
)

// New York session times, in minutes after midnight.
const (
	preOpen    = 4 * 60
	regOpen    = 9*60 + 30
	regClose   = 16 * 60
	postClose  = 20 * 60
	kindPre    = "pre"
	kindPost   = "post"
	chartToday = "1D"
	chartWeek  = "5D"
)

// bar is one intraday price.
type bar struct {
	day   string // YYYYMMDD, New York
	min   int    // minutes after midnight, New York
	price float64
}

// sparkLine is a session's line, as built from bars.
type sparkLine struct {
	Day    string    `json:"day"` // the line's session, YYYYMMDD
	Points []float64 `json:"points"`
	Base   float64   `json:"base,omitempty"` // the close before it
}

// cachedSpark is a symbol's spark as last built from a chart.
type cachedSpark struct {
	Fetched time.Time `json:"fetched"`
	Print   string    `json:"print"` // the quote it was built for
	Line    sparkLine `json:"line"`
	Ext     []float64 `json:"ext,omitempty"`
	Kind    string    `json:"kind,omitempty"` // kindPre or kindPost
	// Prev is the last whole regular session, for drawing before the
	// next one opens.
	Prev *sparkLine `json:"prev,omitempty"`
}

// fingerprint changes whenever a quote's prices or session do.
func fingerprint(q quote) string {
	s := q.Last + "|" + q.MarketStatus
	if q.Extended != nil {
		s += "|" + q.Extended.Last
	}
	return s
}

// newSession reports whether q is in a different session from the quote
// a cached spark was built for (none, after a failed fetch, isn't).
func newSession(print string, q quote) bool {
	parts := strings.Split(print, "|")
	return len(parts) >= 2 && parts[1] != q.MarketStatus
}

// hasExtended reports whether a symbol trades outside its session (US
// shares), which splits its chart into sessions.
func hasExtended(q quote) bool { return q.Extended != nil }

// sparks builds each quote's spark, by symbol, fetching charts as needed.
// Ones that can't be built are left out.
func (m *Monitor) sparks(ctx context.Context, env monitor.Env, quotes []quote) map[string]chipSpark {
	cache := map[string]cachedSpark{}
	if _, err := env.State.Get(sparkKey, &cache); err != nil {
		env.Log.Warn("reading sparks", "err", err)
	}
	now := env.Now()
	out := map[string]chipSpark{}
	changed := false
	for _, q := range quotes {
		c, ok := cache[q.Symbol]
		// A new session (the open, the close) fetches at once; moves
		// within one wait sparkEvery.
		if !ok || c.Print != fingerprint(q) && (now.Sub(c.Fetched) >= sparkEvery || newSession(c.Print, q)) {
			fresh, err := m.buildSpark(ctx, env.HTTP, q, c)
			if err != nil {
				env.Log.Warn("fetching spark", "symbol", q.Symbol, "err", err)
				// Wait sparkEvery before trying again, still drawing
				// any older line meanwhile.
				c.Fetched = now
				cache[q.Symbol] = c
				changed = true
			} else {
				fresh.Fetched, fresh.Print = now, fingerprint(q)
				c, ok = fresh, true
				cache[q.Symbol] = c
				changed = true
			}
		}
		if ok {
			if s := c.spark(q); s != nil {
				out[q.Symbol] = chipSpark{s, c.Kind}
			}
		}
	}
	if changed {
		if err := env.State.Set(sparkKey, cache); err != nil {
			env.Log.Warn("saving sparks", "err", err)
		}
	}
	return out
}

// buildSpark fetches today's chart and builds the spark from it. Before
// the open there's no session today, so the last one comes from old
// (the previous cache) or, failing that, the week's chart.
func (m *Monitor) buildSpark(ctx context.Context, client *http.Client, q quote, old cachedSpark) (cachedSpark, error) {
	bars, err := m.fetchBars(ctx, client, q.Symbol, chartToday)
	if err != nil {
		return cachedSpark{}, err
	}
	prevClose, _ := parseNumber(q.PrevClose)
	c := fromBars(bars, hasExtended(q), prevClose)
	c.Prev = old.Prev
	// No session in today's chart (before the open, or nothing traded
	// yet): the last one.
	if c.Line.Day == "" {
		// Sessions before today's bars, if there are any.
		today := latestDay(bars)
		earlier := func(day string) bool { return day != "" && (today == "" || day < today) }
		if c.Prev == nil || !earlier(c.Prev.Day) {
			week, err := m.fetchBars(ctx, client, q.Symbol, chartWeek)
			if err != nil {
				return cachedSpark{}, err
			}
			w := fromBars(week, hasExtended(q), 0)
			if !earlier(w.Line.Day) {
				return cachedSpark{}, fmt.Errorf("no earlier session in the week's chart")
			}
			c.Prev = &w.Line
		}
		c.Line = *c.Prev
	}
	if c.Line.Day == "" {
		return cachedSpark{}, fmt.Errorf("no bars")
	}
	// A whole regular session is kept for the next morning.
	if c.Kind != kindPre && q.MarketStatus != "REG_MKT" {
		line := c.Line
		c.Prev = &line
	}
	return c, nil
}

// fromBars builds a spark from a chart's bars: the latest session's line
// and, for symbols with extended trading, the after-hours trading that
// followed it or, before the next open, that day's pre-market (after the
// last session in bars, if any). prevClose is the base when the bars hold
// no earlier day.
func fromBars(bars []bar, extended bool, prevClose float64) cachedSpark {
	if len(bars) == 0 {
		return cachedSpark{}
	}
	byDay := map[string][]bar{}
	var days []string
	for _, b := range bars {
		if _, ok := byDay[b.day]; !ok {
			days = append(days, b.day)
		}
		byDay[b.day] = append(byDay[b.day], b)
	}
	sort.Strings(days)
	regular := func(day string) []float64 {
		var out []float64
		for _, b := range byDay[day] {
			if !extended || b.min >= regOpen && b.min < regClose {
				out = append(out, b.price)
			}
		}
		return out
	}
	// The latest day with a session, and the close before it.
	var c cachedSpark
	for i := len(days) - 1; i >= 0; i-- {
		if pts := regular(days[i]); len(pts) > 0 {
			c.Line = sparkLine{Day: days[i], Points: sample(pts, linePoints), Base: prevClose}
			if i > 0 {
				if before := regular(days[i-1]); len(before) > 0 {
					c.Line.Base = before[len(before)-1]
				}
			}
			break
		}
	}
	if !extended {
		return c
	}
	latest := days[len(days)-1]
	var ext []bar
	switch {
	case c.Line.Day == latest:
		for _, b := range byDay[latest] {
			if b.min >= regClose {
				ext = append(ext, b)
			}
		}
		if len(ext) > 0 {
			c.Kind = kindPost
		}
	default:
		for _, b := range byDay[latest] {
			if b.min < regOpen {
				ext = append(ext, b)
			}
		}
		c.Kind = kindPre
	}
	if len(ext) > 0 {
		start, end := preOpen, regOpen
		if c.Kind == kindPost {
			start, end = regClose, postClose
		}
		share := float64(ext[len(ext)-1].min-start) / float64(end-start)
		n := int(math.Round(extPoints * math.Min(math.Max(share, 0), 1)))
		prices := make([]float64, len(ext))
		for i, b := range ext {
			prices[i] = b.price
		}
		c.Ext = sample(prices, max(n, 2))
	}
	return c
}

// latestDay is the last day in bars.
func latestDay(bars []bar) string {
	day := ""
	for _, b := range bars {
		day = max(day, b.day)
	}
	return day
}

// sample picks at most n evenly spaced points, always keeping the first
// and last.
func sample(pts []float64, n int) []float64 {
	if len(pts) <= n || n < 2 {
		return append([]float64(nil), pts...)
	}
	out := make([]float64, n)
	for i := range n {
		out[i] = pts[int(math.Round(float64(i)*float64(len(pts)-1)/float64(n-1)))]
	}
	return out
}

// spark is the cached spark brought up to date with q's live prices.
func (c cachedSpark) spark(q quote) *monitor.Spark {
	if len(c.Line.Points) == 0 {
		return nil
	}
	s := &monitor.Spark{
		Points: append([]float64(nil), c.Line.Points...),
		Tone:   toneOf(q.ChangePct),
		Faded:  q.MarketStatus != "REG_MKT",
	}
	if c.Line.Base > 0 {
		base := c.Line.Base
		s.Base = &base
	}
	if last, ok := parseNumber(q.Last); ok && q.MarketStatus == "REG_MKT" && c.Kind == "" {
		s.Points = append(s.Points, last)
	}
	// Extended trading is never drawn in session: a pre-market line
	// cached before the open is stale until the next fetch.
	if len(c.Ext) > 0 && q.Extended != nil && q.MarketStatus != "REG_MKT" {
		s.Ext = append([]float64(nil), c.Ext...)
		if last, ok := parseNumber(q.Extended.Last); ok && q.MarketStatus != "REG_MKT" {
			s.Ext = append(s.Ext, last)
		}
		s.ExtTone = toneOf(q.Extended.ChangePct)
	}
	return s
}

// chipSpark is a chip's spark and which extended session its Ext is.
type chipSpark struct {
	spark *monitor.Spark
	kind  string
}

// extMove is the extended move a chip leads with while its spark shows
// extended trading: the value and its tag ("pre" or "ah").
func extMove(q quote, cs chipSpark) (string, string, bool) {
	if cs.spark == nil || len(cs.spark.Ext) == 0 || q.Extended == nil || q.Extended.ChangePct == "" {
		return "", "", false
	}
	tag := "ah"
	if cs.kind == kindPre {
		tag = "pre"
	}
	return formatPct(q.Extended.ChangePct), tag, true
}

// fetchBars gets a symbol's intraday bars over a time range: "1D" for
// today's minutes since 04:00 New York, "5D" for the week's in fives.
func (m *Monitor) fetchBars(ctx context.Context, client *http.Client, symbol, timeRange string) ([]bar, error) {
	vars, _ := json.Marshal(map[string]string{"symbol": symbol, "timeRange": timeRange})
	q := url.Values{
		"operationName": {"getQuoteChartData"},
		"variables":     {string(vars)},
		"extensions":    {`{"persistedQuery":{"version":1,"sha256Hash":"9e1670c29a10707c417a1efd327d4b2b1d456b77f1426e7e84fb7d399416bb6b"}}`},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.ChartURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("chart data returned %s", resp.Status)
	}
	var cr struct {
		Data struct {
			ChartData struct {
				PriceBars []struct {
					Open      string `json:"open"`
					Close     string `json:"close"`
					TradeTime string `json:"tradeTime"` // YYYYMMDDhhmmss, New York
				} `json:"priceBars"`
			} `json:"chartData"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decoding chart data: %w", err)
	}
	var out []bar
	for _, b := range cr.Data.ChartData.PriceBars {
		// The last bar is often a running total with no open; the quote
		// has its price anyway.
		open, _ := strconv.ParseFloat(b.Open, 64)
		p, err := strconv.ParseFloat(b.Close, 64)
		if err != nil || p <= 0 || open <= 0 || len(b.TradeTime) < 12 {
			continue
		}
		hh, err1 := strconv.Atoi(b.TradeTime[8:10])
		mm, err2 := strconv.Atoi(b.TradeTime[10:12])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, bar{day: b.TradeTime[:8], min: hh*60 + mm, price: p})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].day != out[j].day {
			return out[i].day < out[j].day
		}
		return out[i].min < out[j].min
	})
	return out, nil
}
