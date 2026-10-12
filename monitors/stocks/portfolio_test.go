package stocks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Monday 12 October 2026, 15:00 in London: both markets are open.
var monday = time.Date(2026, 10, 12, 14, 0, 0, 0, time.UTC)

func quotesBySymbol(qs ...quote) map[string]quote {
	out := map[string]quote{}
	for _, q := range qs {
		out[q.Symbol] = q
	}
	return out
}

var (
	fxQuote   = quote{Symbol: fxSymbol, Last: "1.25", LastTime: "2026-10-12T10:00:00.000-0400"}
	usToday   = quote{Symbol: "AAA", Last: "100.00", Change: "-2.00", ChangePct: "-1.96%", CurrencyCode: "USD", LastTime: "2026-10-12T10:00:00.000-0400"}
	pence     = quote{Symbol: "BBB.-GB", Last: "500.00", Change: "+10.00", ChangePct: "+2.04%", CurrencyCode: "GBp", LastTime: "2026-10-12T14:59:00.000+0100"}
	usFriday  = quote{Symbol: "CCC", Last: "50.00", Change: "+5.00", ChangePct: "+11.11%", CurrencyCode: "USD", LastTime: "2026-10-09"}
	indexOpen = quote{Symbol: ".IDX", Last: "1,000.00", Change: "+10.00", ChangePct: "+1.00%", LastTime: "2026-10-12T14:59:00.000+0100"}
)

func TestEstimate(t *testing.T) {
	p := Portfolio{Cash: 100, Positions: []Position{
		{Symbol: "AAA", Units: 10},                     // $1,000 = £800, down $20 = £16
		{Name: "Bee", Symbol: "bbb.-gb", Units: 200},   // £1,000, up £20
		{Symbol: "CCC", Units: 10},                     // £400; Friday's move doesn't count
		{Name: "Tracker", Symbol: ".IDX", Value: 1000}, // up 1%: £10
		{Name: "Fund", Value: 500},                     // fixed
		{Name: "Gone", Symbol: "ZZZ", Units: 5},        // not quoted
	}}
	e := p.estimate(quotesBySymbol(fxQuote, usToday, pence, usFriday, indexOpen), monday)

	assert.InDelta(t, 100+800+1000+400+1010+500, e.Value, 0.001)
	assert.InDelta(t, -16+20+10, e.Gain, 0.001)
	assert.InDelta(t, 14.0/(800+16+1000-20+400+1000+500)*100, e.Pct, 0.0001, "against the start of the day, without cash")
	assert.True(t, e.Traded)
	assert.Equal(t, []string{"Gone"}, e.Unpriced)
	assert.Equal(t, []contribution{{"Bee", 20}, {"AAA", -16}, {"Tracker", 10}}, e.Top)
}

func TestEstimateBeforeTheOpen(t *testing.T) {
	p := Portfolio{Positions: []Position{{Symbol: "CCC", Units: 10}}}
	saturday := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	e := p.estimate(quotesBySymbol(fxQuote, usFriday), saturday)
	assert.False(t, e.Traded)
	assert.Zero(t, e.Gain)
	assert.InDelta(t, 400, e.Value, 0.001, "valued at the last price")

	// Friday evening in London is still Friday's session.
	friday := time.Date(2026, 10, 9, 21, 30, 0, 0, time.UTC)
	assert.True(t, p.estimate(quotesBySymbol(fxQuote, usFriday), friday).Traded)
}

func TestRecord(t *testing.T) {
	days := record(nil, estimate{Traded: false}, monday)
	assert.Empty(t, days, "nothing traded: nothing to record")

	days = record(days, estimate{Traded: true, Value: 10, Gain: 1}, monday)
	days = record(days, estimate{Traded: true, Value: 12, Gain: 3}, monday.Add(time.Hour))
	assert.Equal(t, []day{{Date: "2026-10-12", Value: 12, Gain: 3}}, days, "today's is replaced")

	for i := range keptDays + 5 {
		days = record(days, estimate{Traded: true}, monday.AddDate(0, 0, i+1))
	}
	assert.Len(t, days, keptDays)
}

func TestPortfolioBlocks(t *testing.T) {
	days := []day{
		{Date: "2026-10-08", Value: 1100, Gain: 100},
		{Date: "2026-10-09", Value: 990, Gain: -110},
		{Date: "2026-10-12", Value: 1000, Gain: 10},
	}
	e := estimate{Value: 1000, Gain: 10, Pct: 1.01, Traded: true, Top: []contribution{{"Bee", 20}, {"AAA", -10}}}
	blocks := portfolioBlocks(e, days, monday)
	require.Len(t, blocks, 3)
	for _, b := range blocks {
		assert.True(t, b.Private)
	}
	assert.Equal(t, "£1,000", blocks[0].Stats[0].Value)
	assert.Equal(t, "+£10", blocks[0].Stats[1].Value)
	assert.Equal(t, "+1.01%", blocks[0].Stats[1].Detail)
	assert.Equal(t, "−£10", blocks[1].Rows[1][1].Text)
	recent := blocks[2].Rows
	require.Len(t, recent, 2, "today's is above, not repeated")
	assert.Equal(t, []string{"Fri 9 Oct", "−£110", "-10.00%", "£990"}, texts(recent[0]))
	assert.Equal(t, monitor.ToneBad, recent[0][1].Tone)

	quiet := portfolioBlocks(estimate{Value: 1000}, days, monday)
	assert.Equal(t, "—", quiet[0].Stats[1].Value)
	assert.Len(t, quiet, 2, "no movers before anything trades")
}

func texts(row []monitor.Cell) []string {
	var out []string
	for _, c := range row {
		out = append(out, c.Text)
	}
	return out
}

func TestFormatPounds(t *testing.T) {
	assert.Equal(t, "£2,345,678", formatPounds(2345678.4))
	assert.Equal(t, "£999", formatPounds(999.4))
	assert.Equal(t, "-£4,321", formatPounds(-4321.2))
	assert.Equal(t, "−£4,321", signedPounds(-4321.2))
	assert.Equal(t, "+£125", signedPounds(125.4))
	assert.Equal(t, "£0", signedPounds(0.2))
}

func TestPortfolioPoll(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := r.URL.Query().Get("symbols"); s != "" {
			asked = s
		}
		var resp quoteResponse
		for _, q := range []quote{usToday, fxQuote} {
			q.Name = q.Symbol
			resp.FormattedQuoteResult.FormattedQuote = append(resp.FormattedQuoteResult.FormattedQuote, q)
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	m := newMonitor(t, srv.URL, map[string]any{
		"symbols":   []string{"AAA"},
		"chartURL":  srv.URL,
		"portfolio": map[string]any{"cash": 5, "positions": []map[string]any{{"symbol": "AAA", "units": 10}}},
	})
	env := monitortest.Env(monday)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	assert.Equal(t, "AAA|GBP=", asked, "the exchange rate is fetched too")
	var symbols []string
	for _, r := range res.Blocks[len(res.Blocks)-1].Rows {
		symbols = append(symbols, r[0].Text)
	}
	assert.Equal(t, []string{"AAA"}, symbols, "but not listed")
	assert.Equal(t, "£805", res.Blocks[0].Stats[0].Value)
	assert.Equal(t, monitor.Stat{Label: "Today", Value: "−£16", Tone: monitor.ToneBad, Private: true}, res.Summary[0])
	assert.False(t, strings.Contains(res.Summary[1].Label, "GBP"))

	var days []day
	_, err = env.State.Get(daysKey, &days)
	require.NoError(t, err)
	assert.Equal(t, []day{{Date: "2026-10-12", Value: 805, Gain: -16}}, days)
}
