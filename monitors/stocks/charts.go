package stocks

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// The open panel's watchlist is a grid of charts, switchable between
// today (the chips' own lines), the week (CNBC's 5D chart, the regular
// sessions' five-minute bars) and the month (its 1M daily closes). The
// week's is fetched at most every weekEvery per symbol, and only once its
// quote has moved; the month's once a day. The live price ends each.

// chartsKey is the state key holding each symbol's week and month.
const chartsKey = "charts"

const (
	weekEvery   = 30 * time.Minute
	retryEvery  = 30 * time.Minute // after a failed fetch
	chartPoints = 60
)

// close is one day's closing price, kept in state.
type closePrice struct {
	Day   string  `json:"day"` // YYYYMMDD
	Price float64 `json:"price"`
}

// history is a symbol's week and month, as last fetched.
type history struct {
	WeekFetched time.Time `json:"weekFetched"`
	WeekPrint   string    `json:"weekPrint"`
	Week        []float64 `json:"week,omitempty"`
	// MonthDay is the New York day the month was fetched for.
	MonthDay   string       `json:"monthDay"`
	MonthTried time.Time    `json:"monthTried"`
	Month      []closePrice `json:"month,omitempty"`
}

// histories fetches each quote's week and month as needed, by symbol.
func (m *Monitor) histories(ctx context.Context, env monitor.Env, quotes []quote) map[string]history {
	all := map[string]history{}
	if _, err := env.State.Get(chartsKey, &all); err != nil {
		env.Log.Warn("reading charts", "err", err)
	}
	now := env.Now()
	today := now.In(newYork).Format("20060102")
	changed := false
	for _, q := range quotes {
		h, ok := all[q.Symbol]
		if !ok || h.WeekPrint != fingerprint(q) && now.Sub(h.WeekFetched) >= weekEvery {
			h.WeekFetched, changed = now, true
			if bars, err := m.fetchBars(ctx, env.HTTP, q.Symbol, chartWeek); err != nil {
				env.Log.Warn("fetching week chart", "symbol", q.Symbol, "err", err)
			} else {
				h.WeekPrint, h.Week = fingerprint(q), weekLine(bars, hasExtended(q))
			}
		}
		if h.MonthDay != today && now.Sub(h.MonthTried) >= retryEvery {
			h.MonthTried, changed = now, true
			if closes, err := m.fetchCloses(ctx, env.HTTP, q.Symbol, "1M"); err != nil {
				env.Log.Warn("fetching month chart", "symbol", q.Symbol, "err", err)
			} else if len(closes) > 0 {
				sort.SliceStable(closes, func(i, j int) bool { return closes[i].day < closes[j].day })
				// One a day: CNBC's running total repeats the latest.
				h.Month = h.Month[:0]
				for _, c := range closes {
					if n := len(h.Month); n > 0 && h.Month[n-1].Day == c.day {
						h.Month[n-1].Price = c.price
						continue
					}
					h.Month = append(h.Month, closePrice{c.day, c.price})
				}

				h.MonthDay = today
			}
		}
		all[q.Symbol] = h
	}
	if changed {
		if err := env.State.Set(chartsKey, all); err != nil {
			env.Log.Warn("saving charts", "err", err)
		}
	}
	return all
}

// weekLine is the week's prices from its bars: the regular sessions only,
// for symbols with extended trading.
func weekLine(bars []bar, extended bool) []float64 {
	var pts []float64
	for _, b := range bars {
		if !extended || b.min >= regOpen && b.min < regClose {
			pts = append(pts, b.price)
		}
	}
	return sample(pts, chartPoints)
}

// chartsBlock is the watchlist as a grid of charts in the table's order,
// with views for today, the week and the month. weekAgo holds closes from
// a week before, by symbol, for the week's move.
func (m *Monitor) chartsBlock(quotes []quote, sparks map[string]chipSpark, hist map[string]history, weekAgo map[string]float64, now time.Time) monitor.Block {
	var day, week, month []monitor.Stat
	for _, q := range m.ordered(quotes) {
		last, okLast := parseNumber(q.Last)
		base := monitor.Stat{Label: displayName(q), Detail: priceText(q), URL: "https://www.cnbc.com/quotes/" + url.PathEscape(q.Symbol)}

		d := m.chip(q, sparks[q.Symbol])
		d.Detail, d.URL = base.Detail, base.URL
		day = append(day, d)

		h := hist[q.Symbol]
		// The week's move is from the close a week ago, as the 1w figure
		// always was, even without the week's line.
		w := base
		if okLast {
			var pts []float64
			if len(h.Week) > 0 {
				pts = append(append(pts, h.Week...), last)
			}
			ref := weekAgo[q.Symbol]
			if ref <= 0 && len(pts) > 0 {
				ref = pts[0]
			}
			if ref > 0 {
				w = moveStat(w, pts, ref, last)
			}
		}
		week = append(week, w)

		mo := base
		if closes := lastMonth(h.Month, now); len(closes) > 0 && okLast {
			var pts []float64
			for _, c := range closes {
				pts = append(pts, c.Price)
			}
			// Today's close is the live price; before today's session the
			// last close is already the latest.
			if tradedToday(q, now) {
				if closes[len(closes)-1].Day == strings.ReplaceAll(q.LastTime[:10], "-", "") {
					pts[len(pts)-1] = last
				} else {
					pts = append(pts, last)
				}
			}
			mo = moveStat(mo, pts, pts[0], last)
		}
		month = append(month, mo)
	}
	return monitor.Block{
		Type:  monitor.BlockStats,
		Title: "Watchlist",
		Chips: true,
		Stats: day,
		Views: []monitor.View{{Label: "Today", Stats: day}, {Label: "1W", Stats: week}, {Label: "1M", Stats: month}},
	}
}

// lastMonth is the closes from the last on or before a month ago: CNBC's
// "1M" is closer to a year.
func lastMonth(closes []closePrice, now time.Time) []closePrice {
	cut := now.In(newYork).AddDate(0, -1, 0).Format("20060102")
	from := 0
	for i, c := range closes {
		if c.Day <= cut {
			from = i
		}
	}
	return closes[from:]
}

// moveStat fills s with the move from ref to last and, given two points
// or more, a line through pts over a faint one at ref.
func moveStat(s monitor.Stat, pts []float64, ref, last float64) monitor.Stat {
	pct := fmt.Sprintf("%+.2f%%", (last/ref-1)*100)
	s.Value, s.Tone = pct, toneOf(pct)
	if len(pts) >= 2 {
		s.Spark = &monitor.Spark{Points: pts, Base: &ref, Tone: s.Tone}
	}
	return s
}

// priceText is a quote's last price, with its currency.
func priceText(q quote) string {
	if q.CurrencyCode == "" {
		return q.Last
	}
	return q.Last + " " + q.CurrencyCode
}
