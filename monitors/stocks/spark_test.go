package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minutes makes bars every five minutes on day from one New York time to
// another (exclusive), priced by f.
func minutes(day string, from, to int, f func(min int) float64) []bar {
	var out []bar
	for m := from; m < to; m += 5 {
		out = append(out, bar{day: day, min: m, price: f(m)})
	}
	return out
}

func flat(p float64) func(int) float64 { return func(int) float64 { return p } }

func TestFromBarsAfterHours(t *testing.T) {
	bars := append(minutes("20261116", preOpen, regOpen, flat(99)), minutes("20261116", regOpen, regClose, flat(100))...)
	bars = append(bars, minutes("20261116", regClose, regClose+120, flat(101))...)
	c := fromBars(bars, true, 98)

	assert.Equal(t, "20261116", c.Line.Day)
	assert.Len(t, c.Line.Points, linePoints)
	assert.Equal(t, 100.0, c.Line.Points[0], "the pre-market isn't part of the line")
	assert.Equal(t, 98.0, c.Line.Base, "no earlier day, so the quote's previous close")
	assert.Equal(t, kindPost, c.Kind)
	// Two of after hours' four hours gone: half its points.
	assert.Len(t, c.Ext, extPoints/2)
	assert.Equal(t, 101.0, c.Ext[0])
}

func TestFromBarsInSession(t *testing.T) {
	bars := append(minutes("20261116", preOpen, regOpen, flat(99)), minutes("20261116", regOpen, regOpen+30, flat(100))...)
	c := fromBars(bars, true, 98)
	assert.Equal(t, "20261116", c.Line.Day)
	assert.Len(t, c.Line.Points, 6)
	assert.Empty(t, c.Kind, "today's pre-market is over once the session's begun")
	assert.Empty(t, c.Ext)
}

func TestFromBarsPreMarket(t *testing.T) {
	// The week's chart: Friday's whole day, Thursday's close before it,
	// and Monday's pre-market so far.
	bars := minutes("20261112", regOpen, regClose, flat(90))
	bars = append(bars, minutes("20261113", preOpen, postClose, func(m int) float64 { return 100 + float64(m-regOpen)/100 })...)
	bars = append(bars, minutes("20261116", preOpen, preOpen+33, flat(105))...)
	c := fromBars(bars, true, 0)

	assert.Equal(t, "20261113", c.Line.Day, "Friday's session")
	assert.Equal(t, 90.0, c.Line.Base, "Thursday's close")
	assert.Equal(t, kindPre, c.Kind)
	assert.Len(t, c.Ext, 2, "a few minutes in: the least there is")

	// Today's chart alone has no session yet.
	today := fromBars(minutes("20261116", preOpen, preOpen+33, flat(105)), true, 0)
	assert.Empty(t, today.Line.Day)
	assert.Equal(t, kindPre, today.Kind)
}

func TestFromBarsNoExtendedTrading(t *testing.T) {
	// London: every bar is the session.
	c := fromBars(minutes("20261116", 3*60, 11*60+30, flat(50)), false, 49)
	assert.Equal(t, "20261116", c.Line.Day)
	assert.Equal(t, 49.0, c.Line.Base)
	assert.Empty(t, c.Kind)
	assert.Empty(t, c.Ext)
}

func TestSample(t *testing.T) {
	assert.Equal(t, []float64{1, 2, 3}, sample([]float64{1, 2, 3}, 5))
	assert.Equal(t, []float64{0, 3, 6, 9}, sample([]float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 4))
}

// chartServer serves CNBC-shaped intraday charts by range, counting the
// requests for each.
type chartServer struct {
	*httptest.Server
	mu     sync.Mutex
	bars   map[string][]bar // by range
	counts map[string]int
}

func newChartServer(t *testing.T, bars map[string][]bar) *chartServer {
	s := &chartServer{bars: bars, counts: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var vars struct{ TimeRange string }
		json.Unmarshal([]byte(r.URL.Query().Get("variables")), &vars)
		s.mu.Lock()
		s.counts[vars.TimeRange]++
		bs := s.bars[vars.TimeRange]
		s.mu.Unlock()
		var parts []string
		for _, b := range bs {
			parts = append(parts, fmt.Sprintf(`{"open":"%.2f","close":"%.2f","tradeTime":"%s%02d%02d00"}`, b.price, b.price, b.day, b.min/60, b.min%60))
		}
		// CNBC's running total, with no open, at the latest price.
		if len(bs) > 0 {
			last := bs[len(bs)-1]
			parts = append(parts, fmt.Sprintf(`{"open":"0.00","close":"%.2f","tradeTime":"%s235900"}`, last.price, last.day))
		}
		fmt.Fprintf(w, `{"data":{"chartData":{"priceBars":[%s]}}}`, strings.Join(parts, ","))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *chartServer) count(r string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[r]
}

func usQuote(status, last, extLast, extPct string) quote {
	q := quote{Symbol: "NVDA", Name: "NVIDIA", Last: last, ChangePct: "-1.10%", PrevClose: "98.00", MarketStatus: status}
	q.Extended = &struct {
		Type      string `json:"type"`
		Last      string `json:"last"`
		Change    string `json:"change"`
		ChangePct string `json:"change_pct"`
	}{Type: status, Last: extLast, ChangePct: extPct}
	return q
}

func TestSparksPreMarketThenSession(t *testing.T) {
	pre := minutes("20261116", preOpen, preOpen+60, flat(105))
	week := append(minutes("20261112", regOpen, regClose, flat(90)), minutes("20261113", regOpen, regClose, flat(100))...)
	week = append(week, pre...)
	srv := newChartServer(t, map[string][]bar{chartToday: pre, chartWeek: week})
	m := newMonitor(t, "unused", map[string]any{"chartURL": srv.URL, "holdings": []string{"NVDA"}})
	// 10:00 in New York is 15:00 in London; 05:00 is 10:00.
	at := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	env := monitortest.Env(at)
	q := usQuote("PRE_MKT", "99.00", "105.00", "+6.06%")

	got := m.sparks(context.Background(), env, []quote{q})["NVDA"]
	require.NotNil(t, got.spark)
	s := got.spark
	assert.Equal(t, kindPre, got.kind)
	assert.Equal(t, 1, srv.count(chartWeek), "no line cached yet, so the week's chart")
	assert.Equal(t, 100.0, s.Points[0], "Friday's session")
	require.NotNil(t, s.Base)
	assert.Equal(t, 90.0, *s.Base)
	assert.True(t, s.Faded, "Friday's over")
	assert.Equal(t, 105.0, s.Ext[len(s.Ext)-1], "the live pre-market price last")
	assert.Equal(t, monitor.ToneGood, s.ExtTone)
	assert.Equal(t, monitor.ToneBad, s.Tone, "Friday's move")

	st := m.summary([]quote{q}, map[string]chipSpark{"NVDA": got})[0]
	assert.Equal(t, "+6.06%", st.Value, "leads with the pre-market move")
	assert.Equal(t, "pre", st.Tag)
	assert.Equal(t, monitor.ToneGood, st.Tone)

	// Unchanged quotes fetch nothing.
	m.sparks(context.Background(), env, []quote{q})
	assert.Equal(t, 1, srv.count(chartToday))

	// Moved, but within five minutes: still nothing.
	q.Extended.Last = "106.00"
	env.Now = func() time.Time { return at.Add(2 * time.Minute) }
	got = m.sparks(context.Background(), env, []quote{q})["NVDA"]
	assert.Equal(t, 1, srv.count(chartToday))
	assert.Equal(t, 106.0, got.spark.Ext[len(got.spark.Ext)-1], "the live price still moves")

	// The session opens: today's line, live, and no extended trading.
	srv.mu.Lock()
	srv.bars[chartToday] = append(pre, minutes("20261116", regOpen, regOpen+30, flat(101))...)
	srv.mu.Unlock()
	q = usQuote("REG_MKT", "101.50", "", "")
	q.ChangePct = "+3.57%"
	env.Now = func() time.Time { return at.Add(6 * time.Hour) }
	got = m.sparks(context.Background(), env, []quote{q})["NVDA"]
	s = got.spark
	assert.Empty(t, got.kind)
	assert.Empty(t, s.Ext)
	assert.False(t, s.Faded)
	assert.Equal(t, 101.5, s.Points[len(s.Points)-1], "the live price last")
	assert.Equal(t, 98.0, *s.Base, "the quote's previous close")
	assert.Equal(t, 1, srv.count(chartWeek))
	st = m.summary([]quote{q}, map[string]chipSpark{"NVDA": got})[0]
	assert.Equal(t, "+3.57%", st.Value)
	assert.Empty(t, st.Tag)
}

func TestSparksKeepTheSessionForTheMorning(t *testing.T) {
	day := append(minutes("20261113", regOpen, regClose, flat(100)), minutes("20261113", regClose, regClose+60, flat(102))...)
	srv := newChartServer(t, map[string][]bar{chartToday: day})
	m := newMonitor(t, "unused", map[string]any{"chartURL": srv.URL})
	at := time.Date(2026, 11, 13, 22, 0, 0, 0, time.UTC)
	env := monitortest.Env(at)
	q := usQuote("POST_MKT", "100.00", "102.00", "+2.00%")

	got := m.sparks(context.Background(), env, []quote{q})["NVDA"]
	assert.Equal(t, kindPost, got.kind)
	assert.Equal(t, "ah", m.summary([]quote{q}, map[string]chipSpark{"NVDA": got})[0].Tag)

	// Monday's pre-market draws Friday's line from the cache, with no
	// week's chart.
	srv.mu.Lock()
	srv.bars[chartToday] = minutes("20261116", preOpen, preOpen+30, flat(103))
	srv.mu.Unlock()
	env.Now = func() time.Time { return at.Add(60 * time.Hour) }
	q = usQuote("PRE_MKT", "100.00", "103.00", "+3.00%")
	got = m.sparks(context.Background(), env, []quote{q})["NVDA"]
	require.NotNil(t, got.spark)
	assert.Equal(t, 0, srv.count(chartWeek))
	assert.Equal(t, 100.0, got.spark.Points[0])
	assert.Equal(t, kindPre, got.kind)
}

func TestSparksBeforeAnyTrades(t *testing.T) {
	// Before anything's traded pre-market, today's chart is empty.
	srv := newChartServer(t, map[string][]bar{chartWeek: minutes("20261113", regOpen, regClose, flat(100))})
	m := newMonitor(t, "unused", map[string]any{"chartURL": srv.URL})
	env := monitortest.Env(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC))
	got := m.sparks(context.Background(), env, []quote{usQuote("PRE_MKT", "100.00", "", "")})["NVDA"]
	require.NotNil(t, got.spark)
	assert.Equal(t, 100.0, got.spark.Points[0])
	assert.Empty(t, got.spark.Ext)
}

func TestSparksBackOffAfterFailing(t *testing.T) {
	srv := newChartServer(t, map[string][]bar{})
	m := newMonitor(t, "unused", map[string]any{"chartURL": srv.URL})
	env := monitortest.Env(time.Date(2026, 11, 16, 15, 0, 0, 0, time.UTC))
	q := usQuote("REG_MKT", "100.00", "", "")
	assert.Empty(t, m.sparks(context.Background(), env, []quote{q}))
	q.Last = "100.50"
	m.sparks(context.Background(), env, []quote{q})
	assert.Equal(t, 1, srv.count(chartToday), "not again for five minutes")
}

func TestChartViews(t *testing.T) {
	week := append(minutes("20261113", preOpen, postClose, flat(100)), minutes("20261116", regOpen, regOpen+60, flat(104))...)
	var month []bar
	// CNBC's "1M" goes back further than a month.
	month = append(month, bar{day: "20260601", price: 10}, bar{day: "20261015", price: 70})
	for i, d := range []string{"20261016", "20261030", "20261113", "20261116"} {
		month = append(month, bar{day: d, price: 80 + float64(i)*10})
	}
	srv := newChartServer(t, map[string][]bar{chartWeek: week, "1M": month})
	m := newMonitor(t, "unused", map[string]any{"chartURL": srv.URL})
	at := time.Date(2026, 11, 16, 15, 30, 0, 0, time.UTC)
	env := monitortest.Env(at)
	q := usQuote("REG_MKT", "105.00", "", "")
	q.LastTime = "2026-11-16"

	hist := m.histories(context.Background(), env, []quote{q})
	b := m.chartsBlock([]quote{q}, nil, hist, map[string]float64{"NVDA": 84}, at)
	require.Len(t, b.Views, 3)

	w := b.Views[1].Stats[0]
	assert.Equal(t, "+25.00%", w.Value, "from the close a week ago")
	require.NotNil(t, w.Spark)
	assert.Equal(t, 104.0, w.Spark.Points[len(w.Spark.Points)-2], "the regular sessions only")
	assert.Equal(t, 105.0, w.Spark.Points[len(w.Spark.Points)-1], "then the live price")
	assert.Equal(t, 84.0, *w.Spark.Base)

	mo := b.Views[2].Stats[0]
	assert.Equal(t, "+31.25%", mo.Value, "from the month's first close")
	assert.Equal(t, []float64{80, 90, 100, 105}, mo.Spark.Points, "today's close is the live price")
	assert.Equal(t, monitor.ToneGood, mo.Tone)

	// Kept: nothing's fetched again until the quote moves and half an hour
	// passes, or for the month, the next day.
	m.histories(context.Background(), env, []quote{q})
	q.Last = "106.00"
	env.Now = func() time.Time { return at.Add(10 * time.Minute) }
	m.histories(context.Background(), env, []quote{q})
	assert.Equal(t, 1, srv.count(chartWeek))
	assert.Equal(t, 1, srv.count("1M"))
	env.Now = func() time.Time { return at.Add(31 * time.Minute) }
	m.histories(context.Background(), env, []quote{q})
	assert.Equal(t, 2, srv.count(chartWeek))
	assert.Equal(t, 1, srv.count("1M"))
}
