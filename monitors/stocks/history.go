package stocks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// defaultChartURL is CNBC's chart data endpoint, which gives daily closes.
const defaultChartURL = "https://webql-redesign.cnbcfm.com/graphql"

// weekKey is the state key holding each symbol's close from a week ago.
const weekKey = "week"

// weekRef is a symbol's close from a week before Day. It only changes once
// a day, so it's fetched once a day and kept.
type weekRef struct {
	Day   string  `json:"day"` // YYYYMMDD, New York time
	Close float64 `json:"close"`
}

type chartResponse struct {
	Data struct {
		ChartData struct {
			PriceBars []struct {
				Close     string `json:"close"`
				TradeTime string `json:"tradeTime"` // YYYYMMDDhhmmss, New York time
			} `json:"priceBars"`
		} `json:"chartData"`
	} `json:"data"`
}

var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return l
}()

// weekAgo returns, for each symbol, its last close on or before the same
// day last week. Ones it can't get are left out, to try again next poll.
func (m *Monitor) weekAgo(ctx context.Context, env monitor.Env, symbols []string) map[string]float64 {
	today := env.Now().In(newYork)
	day := today.Format("20060102")
	refs := map[string]weekRef{}
	if _, err := env.State.Get(weekKey, &refs); err != nil {
		env.Log.Warn("reading week closes", "err", err)
	}
	out := map[string]float64{}
	changed := false
	for _, sym := range symbols {
		if r, ok := refs[sym]; ok && r.Day == day {
			out[sym] = r.Close
			continue
		}
		c, ok := m.closeWeekAgo(ctx, env, sym, today.AddDate(0, 0, -7).Format("20060102"))
		if !ok {
			continue
		}
		refs[sym] = weekRef{Day: day, Close: c}
		out[sym] = c
		changed = true
	}
	if changed {
		if err := env.State.Set(weekKey, refs); err != nil {
			env.Log.Warn("saving week closes", "err", err)
		}
	}
	return out
}

// chartRanges are tried in turn: CNBC often answers one with no bars at
// all while another works.
var chartRanges = []string{"1M", "3M", "1Y"}

// closeWeekAgo finds a symbol's close on or before day.
func (m *Monitor) closeWeekAgo(ctx context.Context, env monitor.Env, symbol, day string) (float64, bool) {
	for _, r := range chartRanges {
		closes, err := m.fetchCloses(ctx, env.HTTP, symbol, r)
		if err != nil {
			env.Log.Warn("fetching week close", "symbol", symbol, "range", r, "err", err)
			continue
		}
		if c, ok := closeOnOrBefore(closes, day); ok {
			return c, true
		}
	}
	return 0, false
}

// dailyClose is one day's closing price.
type dailyClose struct {
	day   string // YYYYMMDD
	price float64
}

// fetchCloses gets a symbol's daily closes over a time range such as "1M".
func (m *Monitor) fetchCloses(ctx context.Context, client *http.Client, symbol, timeRange string) ([]dailyClose, error) {
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
	var cr chartResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decoding chart data: %w", err)
	}
	var out []dailyClose
	for _, b := range cr.Data.ChartData.PriceBars {
		p, err := strconv.ParseFloat(b.Close, 64)
		if err != nil || len(b.TradeTime) < 8 || p <= 0 {
			continue
		}
		out = append(out, dailyClose{b.TradeTime[:8], p})
	}
	return out, nil
}

// closeOnOrBefore finds the latest close on or before day (YYYYMMDD).
// CNBC's bars aren't always in order, so it sorts them first.
func closeOnOrBefore(closes []dailyClose, day string) (float64, bool) {
	sorted := append([]dailyClose(nil), closes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].day < sorted[j].day })
	price, found := 0.0, false
	for _, c := range sorted {
		if c.day > day {
			break
		}
		price, found = c.price, true
	}
	return price, found
}
