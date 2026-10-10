package stocks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 10, 10, 19, 0, 0, 0, time.UTC)

func newMonitor(t *testing.T, srv string, extra map[string]any) *Monitor {
	t.Helper()
	cfg := map[string]any{"symbols": []string{"AAPL", ".FTSE", "TSLA", "BTC.CM=", "BADSYM"}, "quoteURL": srv}
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

func TestMoveEvents(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/quotes.json")
	m := newMonitor(t, srv.URL, map[string]any{"movePct": 1})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
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
	res, err = off.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Empty(t, res.Events)
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
