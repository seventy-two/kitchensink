package farming

import (
	"fmt"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// blocks are a table per run: each patch, what's in it, the pet chance
// and when it's ready (or what it needs).
func (m *Monitor) blocks(b board, now time.Time) []monitor.Block {
	var blocks []monitor.Block
	for i, rows := range b.rows {
		if len(rows) == 0 {
			continue
		}
		run := runs[i]
		title := run.Title
		ch, priced := cheapest(run.Plant, m.prices, b.level, *m.cfg.Protect)
		if priced {
			name := produces[ch.Produce].Name
			if len(run.Plant) > 1 {
				name = "cheapest is " + name
			}
			title += fmt.Sprintf(" · %s, %s a patch", name, gp(ch.Total))
		}
		block := monitor.Block{Type: monitor.BlockTable, Title: title, Columns: []monitor.Column{
			{Title: "Patch", Fill: true}, {Title: "Crop"}, {Title: "Pet", Align: monitor.AlignRight}, {Title: "Ready", Align: monitor.AlignRight},
		}}
		for _, r := range rows {
			crop := monitor.Cell{Text: produces[r.pred.Produce].Name}
			if r.pred.Produce == "WEEDS" || r.pred.State == empty {
				crop = monitor.Cell{Text: patchNames[r.patch.Impl], Tone: monitor.ToneMuted}
			}
			pet := monitor.Cell{Tone: monitor.ToneMuted}
			if n := petChance(r.pred.Produce, b.level); n > 0 && crop.Tone == "" {
				pet.Text = fmt.Sprintf("1/%s", thousands(n))
			}
			text, tone, _ := r.status(now, m.loc)
			block.Rows = append(block.Rows, []monitor.Cell{{Text: r.place()}, crop, pet, {Text: text, Tone: tone}})
		}
		blocks = append(blocks, block)
		if priced && ch.Payment > 0 {
			blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: costs(ch)})
		}
	}
	return blocks
}

// costs breaks down what a crop costs a patch, e.g. "Protection: 9
// sweetcorn, 1k. Sapling: 250."
func costs(ch choice) string {
	plant := "Seed"
	if ch.FromSapling {
		plant = "Sapling"
	}
	return fmt.Sprintf("Protection: %d %s, %s. %s: %s.", ch.Pay, ch.PayName, gp(ch.Payment), plant, gp(ch.Plant))
}

// summary is a chip per run: ready, or when it all will be.
func (m *Monitor) summary(b board, now time.Time) []monitor.Stat {
	var stats []monitor.Stat
	for i, rows := range b.rows {
		if len(rows) == 0 {
			continue
		}
		ready, last := 0, time.Time{}
		for _, r := range rows {
			if _, _, ok := r.status(now, m.loc); ok {
				ready++
			} else if r.growing(now) && r.pred.Done.After(last) {
				last = r.pred.Done
			}
		}
		s := monitor.Stat{Label: runs[i].Short}
		switch {
		case ready == len(rows):
			s.Value, s.Tone = "Ready", monitor.ToneGood
		case last.IsZero():
			s.Value, s.Tone = "—", monitor.ToneMuted
		default:
			s.Value = when(last, now, m.loc)
			if ready > 0 {
				s.Detail, s.Tone = fmt.Sprintf("%d of %d ready", ready, len(rows)), monitor.ToneAccent
			}
		}
		stats = append(stats, s)
	}
	return stats
}

// events says when every patch of a kind (fruit trees, say) has finished
// growing, once per growth: keyed by when the last one finished.
func (m *Monitor) events(b board, now time.Time) []monitor.Event {
	var events []monitor.Event
	for _, rows := range b.rows {
		byImpl := map[string][]row{}
		var order []string
		for _, r := range rows {
			if byImpl[r.patch.Impl] == nil {
				order = append(order, r.patch.Impl)
			}
			byImpl[r.patch.Impl] = append(byImpl[r.patch.Impl], r)
		}
		for _, impl := range order {
			var last time.Time
			var places []string
			waiting := false
			for _, r := range byImpl[impl] {
				if r.growing(now) {
					waiting = true
					break
				}
				if _, _, ready := r.status(now, m.loc); ready {
					places = append(places, r.place())
				}
				if r.pred.Done.After(last) {
					last = r.pred.Done
				}
			}
			// Only crops that grew since RuneLite saw them, and not long
			// ago: an old harvest isn't news.
			if waiting || last.IsZero() || now.Sub(last) > 12*time.Hour {
				continue
			}
			events = append(events, monitor.Event{
				Key:   fmt.Sprintf("ready:%s:%d", impl, last.Unix()),
				Time:  last,
				Title: implNames[impl] + " ready",
				Body:  strings.Join(places, ", "),
			})
		}
	}
	return events
}

// gp writes a price as the game does: 950, 12.5k, 3.2m.
func gp(n int) string {
	switch {
	case n >= 10_000_000:
		return fmt.Sprintf("%dm", n/1_000_000)
	case n >= 1_000_000:
		return trim(fmt.Sprintf("%.1fm", float64(n)/1e6))
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return trim(fmt.Sprintf("%.1fk", float64(n)/1e3))
	}
	return fmt.Sprintf("%d", n)
}

func trim(s string) string { return strings.Replace(s, ".0", "", 1) }

func thousands(n int) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
