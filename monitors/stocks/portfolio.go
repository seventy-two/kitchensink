package stocks

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Portfolio is an account's holdings, for an estimate of its value and the
// day's gain or loss from the same quotes as the watchlist.
type Portfolio struct {
	// Cash is counted in the value but doesn't move.
	Cash float64 `json:"cash"`
	// Positions are the holdings. Each has Units of a CNBC Symbol, priced
	// live, or a Value: fixed, e.g. a fund CNBC doesn't quote, or moving
	// by Symbol's daily percentage if one is given.
	Positions []Position `json:"positions"`
}

// Position is one holding. Values are in pounds.
type Position struct {
	Name   string  `json:"name"`
	Symbol string  `json:"symbol"`
	Units  float64 `json:"units"`
	Value  float64 `json:"value"`
}

// fxSymbol is CNBC's GBP/USD rate, dollars to the pound.
const fxSymbol = "GBP="

// daysKey is the state key for the estimate at the end of each day.
const daysKey = "days"

// day is a day's estimate, as last worked out that day.
type day struct {
	Date  string  `json:"date"` // YYYY-MM-DD, London time
	Value float64 `json:"value"`
	Gain  float64 `json:"gain"`
}

// keptDays is how many days are kept; shownDays how many are listed.
const (
	keptDays  = 30
	shownDays = 7
)

// contribution is a position's share of the day's gain.
type contribution struct {
	Name string
	Gain float64
}

// estimate is the portfolio worked out from the quotes.
type estimate struct {
	Value    float64 // pounds, cash included
	Gain     float64 // today's, pounds
	Pct      float64 // Gain against what was invested at the start of the day
	Traded   bool    // whether any of it has traded today
	Unpriced []string
	Top      []contribution // largest moves first
}

var london = func() *time.Location {
	l, err := time.LoadLocation("Europe/London")
	if err != nil {
		return time.UTC
	}
	return l
}()

// symbols are the ones the portfolio needs quotes for.
func (p *Portfolio) symbols() []string {
	var out []string
	usd := false
	for _, pos := range p.Positions {
		if pos.Symbol != "" {
			out = append(out, pos.Symbol)
		}
		usd = usd || pos.Units > 0
	}
	if usd {
		out = append(out, fxSymbol)
	}
	return out
}

// pounds is the value in pounds of an amount in a quote's currency, and
// whether that's known. CNBC quotes London shares in pence ("GBp").
func pounds(amount float64, q quote, usdPerGBP float64) (float64, bool) {
	switch q.CurrencyCode {
	case "GBP":
		return amount, true
	case "GBp":
		return amount / 100, true
	case "USD":
		if usdPerGBP <= 0 {
			return 0, false
		}
		return amount / usdPerGBP, true
	}
	return 0, false
}

// tradedToday is whether a quote's move is from today's session in
// London: before the open, CNBC still shows the last session's. US
// sessions end before midnight in London, so its date does for both.
func tradedToday(q quote, now time.Time) bool {
	return len(q.LastTime) >= 10 && q.LastTime[:10] == now.In(london).Format(time.DateOnly)
}

// estimate values the portfolio from quotes, by symbol. Like the broker's
// own day gain, it leaves out currency moves.
func (p *Portfolio) estimate(quotes map[string]quote, now time.Time) estimate {
	e := estimate{Value: p.Cash}
	var fx float64
	if q, ok := quotes[fxSymbol]; ok {
		fx, _ = parseNumber(q.Last)
	}
	var start float64 // invested value before today's moves
	for _, pos := range p.Positions {
		name := pos.Name
		if name == "" {
			name = pos.Symbol
		}
		q, quoted := quotes[strings.ToUpper(pos.Symbol)]
		var value, gain float64
		switch {
		case pos.Units > 0:
			last, okLast := parseNumber(q.Last)
			change, okChange := parseNumber(q.Change)
			if q.Change == "UNCH" {
				change, okChange = 0, true
			}
			v, okV := pounds(pos.Units*last, q, fx)
			g, okG := pounds(pos.Units*change, q, fx)
			if !quoted || !okLast || !okChange || !okV || !okG {
				e.Unpriced = append(e.Unpriced, name)
				continue
			}
			value = v
			if tradedToday(q, now) {
				gain = g
			}
		default:
			value = pos.Value
			if pct, ok := parsePct(q.ChangePct); quoted && ok && tradedToday(q, now) {
				gain = value * pct / 100
				value += gain
			}
		}
		if quoted && tradedToday(q, now) {
			e.Traded = true
		}
		e.Value += value
		e.Gain += gain
		start += value - gain
		if gain != 0 {
			e.Top = append(e.Top, contribution{Name: name, Gain: gain})
		}
	}
	if start > 0 {
		e.Pct = e.Gain / start * 100
	}
	sort.SliceStable(e.Top, func(i, j int) bool { return math.Abs(e.Top[i].Gain) > math.Abs(e.Top[j].Gain) })
	return e
}

// record keeps today's estimate in days, newest last, once anything has
// traded today.
func record(days []day, e estimate, now time.Time) []day {
	if !e.Traded {
		return days
	}
	today := day{Date: now.In(london).Format(time.DateOnly), Value: e.Value, Gain: e.Gain}
	if n := len(days); n > 0 && days[n-1].Date == today.Date {
		days[n-1] = today
	} else {
		days = append(days, today)
	}
	return days[max(0, len(days)-keptDays):]
}

// topLen is how many of the day's biggest contributions are listed.
const topLen = 5

// portfolioBlocks shows the estimate: the value and today's move, the
// positions that moved it most, and recent days. They're private, for
// clients to hide on request.
func portfolioBlocks(e estimate, days []day, now time.Time) []monitor.Block {
	today := monitor.Stat{Label: "Today", Value: signedPounds(e.Gain), Detail: fmt.Sprintf("%+.2f%%", e.Pct), Tone: toneOfFloat(e.Gain)}
	if !e.Traded {
		today = monitor.Stat{Label: "Today", Value: "—", Detail: "nothing has traded yet", Tone: monitor.ToneMuted}
	}
	value := monitor.Stat{Label: "Portfolio", Value: formatPounds(e.Value)}
	if len(e.Unpriced) > 0 {
		value.Detail = "without " + strings.Join(e.Unpriced, ", ")
		value.Tone = monitor.ToneWarn
	}
	blocks := []monitor.Block{{Type: monitor.BlockStats, Private: true, Stats: []monitor.Stat{value, today}}}

	if e.Traded && len(e.Top) > 0 {
		top := monitor.Block{Type: monitor.BlockTable, Title: "Moving it most today", Private: true,
			Columns: []monitor.Column{{Title: "", Fill: true}, {Title: "", Align: monitor.AlignRight}}}
		for _, c := range e.Top[:min(topLen, len(e.Top))] {
			top.Rows = append(top.Rows, []monitor.Cell{{Text: c.Name}, {Text: signedPounds(c.Gain), Tone: toneOfFloat(c.Gain)}})
		}
		blocks = append(blocks, top)
	}

	// Earlier days, newest first; today's is above.
	todayDate := now.In(london).Format(time.DateOnly)
	recent := monitor.Block{Type: monitor.BlockTable, Title: "Recent days", Private: true,
		Columns: []monitor.Column{{Title: "Day"}, {Title: "Gain", Align: monitor.AlignRight},
			{Title: "%", Align: monitor.AlignRight}, {Title: "Value", Align: monitor.AlignRight}}}
	for i := len(days) - 1; i >= 0 && len(recent.Rows) < shownDays; i-- {
		d := days[i]
		if d.Date == todayDate {
			continue
		}
		date, err := time.Parse(time.DateOnly, d.Date)
		if err != nil {
			continue
		}
		pct := ""
		if start := d.Value - d.Gain; start > 0 {
			pct = fmt.Sprintf("%+.2f%%", d.Gain/start*100)
		}
		tone := toneOfFloat(d.Gain)
		recent.Rows = append(recent.Rows, []monitor.Cell{
			{Text: date.Format("Mon 2 Jan")},
			{Text: signedPounds(d.Gain), Tone: tone},
			{Text: pct, Tone: tone},
			{Text: formatPounds(d.Value), Tone: monitor.ToneMuted},
		})
	}
	if len(recent.Rows) > 0 {
		blocks = append(blocks, recent)
	}
	return blocks
}

// portfolioStat is the summary's chip for the day.
func portfolioStat(e estimate) monitor.Stat {
	if !e.Traded {
		return monitor.Stat{Label: "Today", Value: "—", Tone: monitor.ToneMuted, Private: true}
	}
	return monitor.Stat{Label: "Today", Value: signedPounds(e.Gain), Tone: toneOfFloat(e.Gain), Private: true}
}

func toneOfFloat(v float64) monitor.Tone {
	switch {
	case math.Round(v*100) > 0:
		return monitor.ToneGood
	case math.Round(v*100) < 0:
		return monitor.ToneBad
	}
	return monitor.ToneNone
}

// formatPounds is a whole number of pounds with thousands separators,
// e.g. "£2,345,678".
func formatPounds(v float64) string {
	s := fmt.Sprintf("%.0f", math.Abs(v))
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	sign := ""
	if math.Round(v) < 0 {
		sign = "-"
	}
	return sign + "£" + b.String()
}

// signedPounds is formatPounds with a sign either way, e.g. "+£125".
func signedPounds(v float64) string {
	if math.Round(v) <= 0 {
		return strings.Replace(formatPounds(v), "-", "−", 1)
	}
	return "+" + formatPounds(v)
}
