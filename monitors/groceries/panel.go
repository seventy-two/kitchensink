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
	lasting := map[string]bool{}
	soonLimit := today.AddDate(0, 0, m.soon)
	for _, it := range items {
		useBy := it.useBy(m.loc)
		if it.staple() || useBy.After(today.AddDate(0, 0, m.show)) {
			lasting[strings.ToLower(it.Name)] = true
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
			{Text: name, Detail: detail(it)},
			{Text: m.when(useBy, today), Tone: tone},
			{Action: &monitor.CellAction{Type: UsedAction, ID: it.ID, Label: "Used up or thrown away"}},
		}
		table.Rows = append(table.Rows, row)
	}
	switch {
	case len(table.Rows) > 0:
		blocks = append(blocks, table)
	case pending == 0:
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: "Nothing from the shopping that goes off soon."})
	}
	if n := len(lasting); n > 0 {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText,
			Text: fmt.Sprintf("And %d longer-lasting %s from the shopping.", n, plural(n, "thing", "things"))})
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

	// The short view is a chip for each thing going off soon.
	return monitor.Result{Blocks: blocks, Summary: m.summary(items, today), SummaryStyle: monitor.SummaryChips}
}

// summary is a chip for each thing going off soon, soonest first: its
// name and day, red on its last day. Brief leaves the day out after
// tomorrow.
func (m *Monitor) summary(items []Item, today time.Time) []monitor.Stat {
	soon := m.goingOff(items, today)
	if len(soon) == 0 {
		return []monitor.Stat{{Label: "Food", Value: "Nothing going off", Tone: monitor.ToneMuted}}
	}
	var out []monitor.Stat
	for _, it := range soon {
		useBy := it.useBy(m.loc)
		st := monitor.Stat{Label: it.Name, Value: m.when(useBy, today), Tone: monitor.ToneWarn}
		if useBy.Before(today.AddDate(0, 0, 1)) {
			st.Tone = monitor.ToneBad
		}
		// One-line summaries name the day only when it's close (the
		// user's ask): later days are just the name.
		if !useBy.Before(today.AddDate(0, 0, 2)) {
			st.Brief = new(string)
		}
		out = append(out, st)
	}
	return out
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

// detail is where it's kept, and whether it has egg in it.
func detail(it Item) string {
	var parts []string
	if it.Storage != "" {
		parts = append(parts, capitalise(it.Storage))
	}
	if it.Egg {
		parts = append(parts, "has egg")
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
