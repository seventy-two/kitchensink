package groceries

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// result is the panel: what's going off, by day, each with a button to
// say it's been used, then the recipes.
func (m *Monitor) result(items []Item, rs recipes, problem string, pending int, today time.Time) monitor.Result {
	var blocks []monitor.Block
	if problem != "" {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: problem})
	}
	if pending > 0 {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: "Reading the latest order…"})
	}
	table := monitor.Block{
		Type:    monitor.BlockTable,
		Columns: []monitor.Column{{Title: "Food", Fill: true}, {Title: "Use by", Align: monitor.AlignRight}, {Title: ""}},
		Rows:    [][]monitor.Cell{},
	}
	short := table
	short.Rows = [][]monitor.Cell{}
	lasting := 0
	soonLimit := today.AddDate(0, 0, m.soon)
	for _, it := range items {
		useBy := it.useBy(m.loc)
		if useBy.After(today.AddDate(0, 0, m.show)) {
			lasting++
			continue
		}
		tone := monitor.ToneNone
		switch {
		case useBy.Before(today.AddDate(0, 0, 1)):
			tone = monitor.ToneBad
		case !useBy.After(soonLimit):
			tone = monitor.ToneWarn
		}
		name := it.Name
		if it.Qty > 1 {
			name = fmt.Sprintf("%s ×%d", name, it.Qty)
		}
		row := []monitor.Cell{
			{Text: name, Detail: storage(it.Storage)},
			{Text: m.when(useBy, today), Tone: tone},
			{Action: &monitor.CellAction{Type: UsedAction, ID: it.ID, Label: "Used up or thrown away"}},
		}
		table.Rows = append(table.Rows, row)
		if !useBy.After(soonLimit) {
			short.Rows = append(short.Rows, row)
		}
	}
	switch {
	case len(table.Rows) > 0:
		blocks = append(blocks, table)
	case pending == 0:
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: "Nothing from the shopping that goes off soon."})
	}
	if lasting > 0 {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText,
			Text: fmt.Sprintf("And %d longer-lasting %s from the shopping.", lasting, plural(lasting, "thing", "things"))})
	}
	var recipe monitor.Block
	if len(rs.List) > 0 {
		recipe = monitor.Block{Type: monitor.BlockList, Title: "Recipes to use them up"}
		for _, r := range rs.List {
			sub := "Uses " + strings.ToLower(strings.Join(r.Uses, ", "))
			if len(r.Buy) > 0 {
				sub += " · buy " + strings.ToLower(strings.Join(r.Buy, ", "))
			}
			title := r.Title
			if r.Minutes > 0 {
				title = fmt.Sprintf("%s (%d min)", title, r.Minutes)
			}
			recipe.Items = append(recipe.Items, monitor.Item{Title: title, Subtitle: sub, Detail: r.Method})
		}
		blocks = append(blocks, recipe)
	}

	res := monitor.Result{Blocks: blocks, Summary: m.summary(items, today)}
	if len(short.Rows) > 0 {
		res.SummaryBlock = &short
	}
	return res
}

// summary is the next thing going off, and how many more go soon.
func (m *Monitor) summary(items []Item, today time.Time) []monitor.Stat {
	soon := m.goingOff(items, today)
	if len(soon) == 0 {
		return []monitor.Stat{{Label: "Food", Value: "Nothing going off", Tone: monitor.ToneMuted}}
	}
	first := soon[0]
	useBy := first.useBy(m.loc)
	st := monitor.Stat{Label: "Use by " + m.when(useBy, today), Value: first.Name, Tone: monitor.ToneWarn}
	if useBy.Before(today.AddDate(0, 0, 1)) {
		st.Tone = monitor.ToneBad
	}
	if n := len(soon) - 1; n > 0 {
		st.Detail = fmt.Sprintf("+%d more by %s", n, m.when(soon[n].useBy(m.loc), today))
	}
	return []monitor.Stat{st}
}

// when names a use-by day: gone off, today, tomorrow, a weekday this
// week, else the date.
func (m *Monitor) when(d, today time.Time) string {
	days := int(math.Round(d.Sub(today).Hours() / 24))
	switch {
	case days < 0:
		return "Gone off"
	case days == 0:
		return "Today"
	case days == 1:
		return "Tomorrow"
	case days < 7:
		return d.Format("Monday")
	}
	return d.Format("2 Jan")
}

func storage(s string) string {
	if s == "" {
		return ""
	}
	return capitalise(s)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
