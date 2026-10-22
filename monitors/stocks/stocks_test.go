package stocks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 10, 10, 19, 0, 0, 0, time.UTC)

// inSession is during the fixture's trading day, Friday 9 October, with
// London and New York both open.
var inSession = time.Date(2026, 10, 9, 15, 30, 0, 0, time.UTC)

func newMonitor(t *testing.T, srv string, extra map[string]any) *Monitor {
	t.Helper()
	chart := monitortest.Serve(t, "testdata/chart.json")
	cfg := map[string]any{"symbols": []string{"AAPL", ".FTSE", "TSLA", "BTC.CM=", "BADSYM"}, "quoteURL": srv, "chartURL": chart.URL}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestPollTable(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, nil)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	q, _ := url.ParseQuery(srv.Requests()[0].Query)
	assert.Equal(t, "AAPL|.FTSE|TSLA|BTC.CM=|BADSYM", q.Get("symbols"))

	require.Len(t, res.Blocks, 1)
	rows := res.Blocks[0].Rows
	// Unknown symbols are dropped and the rest sorted by the day's move.
	require.Len(t, rows, 4)
	var order []string
	for _, r := range rows {
		order = append(order, r[0].Text)
	}
	assert.Equal(t, []string{"TSLA", ".FTSE", "BTC.CM=", "AAPL"}, order)

	aapl := rows[3]
	assert.Equal(t, "336.64", aapl[2].Text)
	assert.Equal(t, monitor.ToneBad, aapl[4].Tone)
	assert.Equal(t, "336.08 -0.17%", aapl[5].Text, "after-hours price shown outside the regular session")
	assert.Equal(t, "", rows[1][5].Text, "no extended price for the FTSE")
	assert.Empty(t, res.Events, "no move reaches the default 5%")
}

// openQuotes is the quotes fixture with every market in its regular
// session, saved to a temporary file.
func openQuotes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/quotes.json")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "quotes.json")
	require.NoError(t, os.WriteFile(path, []byte(strings.ReplaceAll(string(b), `"POST_MKT"`, `"REG_MKT"`)), 0o600))
	return path
}

func TestMoveEvents(t *testing.T) {
	srv := monitortest.Serve(t, openQuotes(t))
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1})
	res, err := m.Poll(context.Background(), monitortest.Env(inSession))
	require.NoError(t, err)

	// TSLA +2.05% passes 2%; the FTSE +1.06% and AAPL -1.11% pass 1%.
	assert.ElementsMatch(t, []string{
		"move:AAPL:2026-10-09:down1",
		"move:.FTSE:2026-10-09:up1",
		"move:TSLA:2026-10-09:up2",
	}, monitortest.Keys(res.Events))
	for _, e := range res.Events {
		switch e.Key {
		case "move:TSLA:2026-10-09:up2":
			assert.Equal(t, "TSLA up 2.05% today", e.Title)
			assert.Equal(t, "382.70 USD (+7.70)", e.Body)
			assert.Equal(t, monitor.PriorityHigh, e.Priority, "two steps is high priority")
		case "move:.FTSE:2026-10-09:up1":
			assert.Equal(t, "FTSE 100 up 1.06% today", e.Title, "indices use their name")
			assert.Equal(t, monitor.PriorityNormal, e.Priority)
		}
	}

	off := newMonitor(t, srv.URL, map[string]any{"movePct": 0})
	res, err = off.Poll(context.Background(), monitortest.Env(inSession))
	require.NoError(t, err)
	assert.Empty(t, res.Events)

	held := newMonitor(t, srv.URL, map[string]any{"movePct": 1, "holdings": []string{"tsla"}})
	res, err = held.Poll(context.Background(), monitortest.Env(inSession))
	require.NoError(t, err)
	assert.Equal(t, []string{"move:TSLA:2026-10-09:up2"}, monitortest.Keys(res.Events), "only holdings notify")
}

func TestMovesOnlyInTheSession(t *testing.T) {
	// AAPL and TSLA have closed; the FTSE is open.
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1})
	res, err := m.Poll(context.Background(), monitortest.Env(inSession))
	require.NoError(t, err)
	assert.Equal(t, []string{"move:.FTSE:2026-10-09:up1"}, monitortest.Keys(res.Events))
}

func TestMovesOnlyFromToday(t *testing.T) {
	// Early on a later morning CNBC has sent an old day's quotes marked as
	// in session: their moves aren't today's, so they don't notify.
	srv := monitortest.Serve(t, openQuotes(t))
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1})
	res, err := m.Poll(context.Background(), monitortest.Env(time.Date(2026, 10, 13, 6, 34, 0, 0, time.UTC)))
	require.NoError(t, err)
	assert.Empty(t, res.Events)
}

func TestMovesStayUpToDate(t *testing.T) {
	path := openQuotes(t)
	srv := monitortest.Serve(t, path)
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1, "holdings": []string{"tsla"}})
	env := monitortest.Env(inSession)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	var news, updates []monitor.Event
	for _, e := range res.Events {
		if e.Update {
			updates = append(updates, e)
		} else {
			news = append(news, e)
		}
	}
	require.Equal(t, []string{"move:TSLA:2026-10-09:up2"}, monitortest.Keys(news))
	assert.Empty(t, updates, "a move just raised needs no update")

	// Still up 2.05%: the event again (which the engine drops as a repeat)
	// and its update, saying the same, so changing nothing.
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.False(t, res.Events[0].Update)
	assert.True(t, res.Events[1].Update)
	assert.Equal(t, "TSLA up 2.05% today", res.Events[1].Title)
	assert.False(t, res.Events[1].Quiet)

	// Later TSLA has fallen back to +0.50%: the event says so, quietly.
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	srv.Reply(http.StatusOK, []byte(strings.Replace(strings.Replace(string(b), `"+2.05%"`, `"+0.50%"`, 1), `"+7.70"`, `"+1.90"`, 1)))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	e := res.Events[0]
	assert.True(t, e.Update)
	assert.Equal(t, "move:TSLA:2026-10-09:up2", e.Key)
	assert.Equal(t, "TSLA up 0.50% today", e.Title)
	assert.Equal(t, "Earlier up 2.05% · 382.70 USD (+1.90)", e.Body)
	assert.True(t, e.Quiet, "muted once it's fallen back")

	// The next day's first poll forgets it.
	next := monitortest.Env(inSession.AddDate(0, 0, 3))
	next.State = env.State
	_, err = m.Poll(context.Background(), next)
	require.NoError(t, err)
	var moves map[string]notedMove
	_, err = env.State.Get(movesKey, &moves)
	require.NoError(t, err)
	assert.Empty(t, moves)
}

func TestHigherMovesReplaceLower(t *testing.T) {
	path := openQuotes(t)
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	srv := monitortest.Serve(t, path)
	srv.Reply(http.StatusOK, []byte(strings.Replace(string(b), `"+2.05%"`, `"+1.50%"`, 1)))
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1, "holdings": []string{"tsla"}})
	env := monitortest.Env(inSession)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"move:TSLA:2026-10-09:up1"}, monitortest.Keys(res.Events))

	// Up 2.05%: the new level notifies and the old one's event ends now.
	srv.Reply(http.StatusOK, b)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.Equal(t, "move:TSLA:2026-10-09:up2", res.Events[0].Key)
	assert.False(t, res.Events[0].Update)
	assert.Equal(t, monitor.Event{Key: "move:TSLA:2026-10-09:up1", Update: true, Title: "TSLA up 2.05% today",
		Body: "382.70 USD (+7.70)", Until: inSession}, res.Events[1])

	// And it's forgotten: only the new one is kept up to date.
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{"move:TSLA:2026-10-09:up2", "move:TSLA:2026-10-09:up2"}, monitortest.Keys(res.Events))
}

func TestTargetsNotifyOncePerCrossing(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, map[string]any{
		"targets": map[string]any{
			"AAPL": map[string]any{"above": 300, "below": 330},
			"TSLA": map[string]any{"above": 400},
		},
	})
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	// AAPL at 336.64 is above 300; nothing else is past its target.
	require.Len(t, res.Events, 1)
	assert.Equal(t, "AAPL is above 300", res.Events[0].Title)
	assert.Equal(t, monitor.PriorityHigh, res.Events[0].Priority)

	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "still above: no repeat")

	// It drops below 330 (crossing the below target and leaving 300's
	// "above" set), then recovers: the above target fires again.
	body := quotesWithAAPL(t, "320.00")
	srv.Reply(http.StatusOK, body)
	env.Now = func() time.Time { return now.Add(time.Hour) }
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "AAPL is below 330", res.Events[0].Title)
}

func quotesWithAAPL(t *testing.T, last string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/quotes.json")
	require.NoError(t, err)
	var raw map[string]map[string][]map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	for _, q := range raw["FormattedQuoteResult"]["FormattedQuote"] {
		if q["symbol"] == "AAPL" {
			q["last"] = last
		}
	}
	b, _ = json.Marshal(raw)
	return b
}

func TestPollErrors(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, nil)

	srv.Reply(http.StatusForbidden, []byte("<HTML>Access Denied</HTML>"))
	_, err := m.Poll(context.Background(), monitortest.Env(now))
	assert.ErrorContains(t, err, "403")

	srv.Reply(http.StatusOK, []byte(`{"FormattedQuoteResult":{"FormattedQuote":[{"symbol":"NOPE","code":1}]}}`))
	_, err = m.Poll(context.Background(), monitortest.Env(now))
	assert.ErrorContains(t, err, "no quotes")
}

func TestNewNeedsSymbols(t *testing.T) {
	_, err := New(json.RawMessage(`{}`))
	assert.Error(t, err)
	_, err = New(json.RawMessage(`{"symbols":["AAPL"],"typo":1}`))
	assert.Error(t, err, "unknown fields are rejected")
}

func TestParse(t *testing.T) {
	v, ok := parseNumber("10,552.05")
	assert.True(t, ok)
	assert.Equal(t, 10552.05, v)
	v, ok = parsePct("UNCH")
	assert.True(t, ok)
	assert.Zero(t, v)
	v, ok = parsePct("-1.11%")
	assert.True(t, ok)
	assert.Equal(t, -1.11, v)
	_, ok = parsePct("")
	assert.False(t, ok)
}

func TestFormatPct(t *testing.T) {
	assert.Equal(t, "+5.48%", formatPct("+5.4756%"))
	assert.Equal(t, "-1.06%", formatPct("-1.06%"))
	assert.Equal(t, "UNCH", formatPct("UNCH"))
	assert.Equal(t, "", formatPct(""))
}

func TestSummary(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	res, err := newMonitor(t, srv.URL, map[string]any{"symbols": []string{"AAPL"}}).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	var got []string
	for _, s := range res.Summary {
		got = append(got, s.Label+" "+s.Value)
	}
	assert.Equal(t, []string{"TSLA +2.05%", "AAPL -1.11%", "FTSE 100 +1.06%", "Bitcoin/USD Coin Metrics +0.91%"}, got,
		"the biggest moves either way")
	assert.Equal(t, monitor.ToneBad, res.Summary[1].Tone)
	assert.Empty(t, res.Summary[1].Detail, "chips leave out the price")
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
}

func TestHoldings(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, map[string]any{"symbols": []string{"AAPL", "TSLA"}, "holdings": []string{".FTSE", "aapl", "NVDA"}})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	var order []string
	for _, r := range res.Blocks[0].Rows {
		order = append(order, r[0].Text)
	}
	assert.Equal(t, []string{".FTSE", "AAPL", "TSLA", "BTC.CM="}, order, "holdings first, largest first, then the biggest risers")
	assert.Equal(t, monitor.ToneAccent, res.Blocks[0].Rows[0][0].Tone, "holdings highlighted")
	assert.Equal(t, monitor.ToneNone, res.Blocks[0].Rows[2][0].Tone)

	var summary []string
	for _, s := range res.Summary {
		summary = append(summary, s.Label)
	}
	assert.Equal(t, []string{"FTSE 100", "AAPL", "TSLA", "Bitcoin/USD Coin Metrics"}, summary, "every quote, in the table's order")
	assert.Contains(t, srv.Requests()[0].Query, "NVDA", "holdings are watched even when not in symbols")
}

func TestWeekChange(t *testing.T) {
	quotes := monitortest.Serve(t, "testdata/quotes.json")
	chart := monitortest.Serve(t, "testdata/chart.json")
	m := newMonitor(t, quotes.URL, map[string]any{"chartURL": chart.URL})
	env := monitortest.Env(now) // Saturday 10 October

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	b := res.Blocks[0]
	assert.Equal(t, "1w", b.Columns[6].Title, "on the far right")
	var aapl []monitor.Cell
	for _, r := range b.Rows {
		if r[0].Text == "AAPL" {
			aapl = r
		}
	}
	require.NotNil(t, aapl)
	// Against Friday 2 October's close of 200, the last on or before a
	// week ago; the out-of-order bar for 1 October doesn't count.
	assert.Equal(t, "+68.32%", aapl[6].Text)
	assert.Equal(t, monitor.ToneGood, aapl[6].Tone)
	fetched := len(chart.Requests())
	assert.Equal(t, 4, fetched, "one fetch per quoted symbol")
	assert.Contains(t, chart.Requests()[0].Query, "getQuoteChartData")

	// The closes are kept for the day.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, chart.Requests(), fetched, "no refetch the same day")
	next := env
	next.Now = func() time.Time { return now.Add(24 * time.Hour) }
	_, err = m.Poll(context.Background(), next)
	require.NoError(t, err)
	assert.Len(t, chart.Requests(), 2*fetched, "fetched again the next day")
}

func TestWeekChangeUnavailable(t *testing.T) {
	quotes := monitortest.Serve(t, "testdata/quotes.json")
	chart := monitortest.Serve(t, "testdata/chart.json")
	chart.Reply(http.StatusForbidden, []byte("Access Denied"))
	m := newMonitor(t, quotes.URL, map[string]any{"chartURL": chart.URL})
	env := monitortest.Env(now)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err, "the quotes still show")
	for _, r := range res.Blocks[0].Rows {
		assert.Empty(t, r[6].Text)
	}
	// Each range was tried, and nothing was kept, so the next poll tries
	// again.
	tries := len(chart.Requests())
	assert.Equal(t, 4*len(chartRanges), tries)
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, chart.Requests(), 2*tries)
}

func TestWeekChangeFallsBack(t *testing.T) {
	// The first range comes back empty, as CNBC's sometimes does; the next
	// has the closes.
	quotes := monitortest.Serve(t, "testdata/quotes.json")
	full, _ := os.ReadFile("testdata/chart.json")
	chart := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("variables"), `"1M"`) {
			w.Write([]byte(`{"data":{"chartData":{"priceBars":[]}}}`))
			return
		}
		w.Write(full)
	}))
	t.Cleanup(chart.Close)
	m := newMonitor(t, quotes.URL, map[string]any{"chartURL": chart.URL})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	for _, r := range res.Blocks[0].Rows {
		if r[0].Text == "AAPL" {
			assert.Equal(t, "+68.32%", r[6].Text)
		}
	}
}

func TestCloseOnOrBefore(t *testing.T) {
	closes := []dailyClose{{"20261005", 3}, {"20261001", 1}, {"20261002", 2}}
	c, ok := closeOnOrBefore(closes, "20261004")
	assert.True(t, ok)
	assert.Equal(t, 2.0, c)
	_, ok = closeOnOrBefore(closes, "20260930")
	assert.False(t, ok, "nothing that early")
}
