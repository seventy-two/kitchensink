package osrs

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
)

// Live updates come from Dink (https://github.com/pajlads/DinkPlugin), a
// RuneLite plugin that sends Discord webhooks as things happen in game:
// kill counts, loot, clues, pets, and the stats at login. Pointed at the
// monitor's hook, it keeps the panel up to date between Wise Old Man
// updates. See https://github.com/pajlads/DinkPlugin/blob/master/docs/json-examples.md.

// dink is the part of a Dink notification the monitor reads.
type dink struct {
	Type       string `json:"type"`
	PlayerName string `json:"playerName"`
	// Seasonal is a league or Deadman world. Dink's docs show it as a
	// string; the plugin sends a boolean.
	Seasonal any             `json:"seasonalWorld"`
	Extra    json.RawMessage `json:"extra"`
}

type dinkItem struct {
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	PriceEach int64  `json:"priceEach"`
}

// drop is a big loot drop or a pet, kept for the day.
type drop struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source,omitempty"`
	// Item is the most valuable item, with how many if more than one.
	Item  string `json:"item"`
	Value int64  `json:"value,omitempty"`
	KC    int64  `json:"kc,omitempty"`
	Pet   bool   `json:"pet,omitempty"`
	Dupe  bool   `json:"dupe,omitempty"`
}

// today is what Dink has told the monitor today, beyond counts.
type today struct {
	Day   string `json:"day"`
	Drops []drop `json:"drops,omitempty"`
	// Starts are kill counts that hiscores don't rank yet (so the day's
	// baseline has 0): the count before the first kill Dink saw today.
	Starts stats `json:"starts,omitempty"`
}

const (
	liveKey  = "live"
	todayKey = "today"
	// lastBigKey holds, by metric, the kill count of the last drop worth
	// bigLoot: from Dink, or set with a "lastBig" action (e.g. from
	// RuneLite's Loot Logger history).
	lastBigKey = "lastBig"
)

// HookSecret is the secret Dink's webhook URL carries.
func (m *Monitor) HookSecret() string { return m.cfg.HookSecret }

// Act takes Dink's notifications. Other players' (an alt on the same
// RuneLite) and those from leagues or Deadman are ignored, as are kinds
// the panel doesn't use.
//
// A "lastBig" action sets the kill count of the last big drop by metric,
// as data {"vorkath": 1020}; later ones win.
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	var lastBig stats
	if _, err := env.State.Get(lastBigKey, &lastBig); err != nil {
		return err
	}
	if lastBig == nil {
		lastBig = stats{}
	}
	if a.Type == "lastBig" {
		var set stats
		if err := json.Unmarshal(a.Data, &set); err != nil {
			return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
		}
		for id, kc := range set {
			if mt, ok := tracked[id]; !ok || mt.Kind == xp {
				return fmt.Errorf("%w: no boss %q", monitor.ErrBadAction, id)
			}
			lastBig[id] = max(lastBig[id], kc)
		}
		return env.State.Set(lastBigKey, lastBig)
	}
	if a.Type != monitor.HookAction {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
	}
	var d dink
	if err := json.Unmarshal(a.Data, &d); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	if !samePlayer(d.PlayerName, m.cfg.Player) || d.Seasonal == true || d.Seasonal == "true" {
		return nil
	}
	now := env.Now().In(m.loc)
	day := now.Format(time.DateOnly)

	var live stats
	if _, err := env.State.Get(liveKey, &live); err != nil {
		return err
	}
	if live == nil {
		live = stats{}
	}
	var td today
	if _, err := env.State.Get(todayKey, &td); err != nil {
		return err
	}
	if td.Day != day {
		td = today{Day: day}
	}
	var base baseline
	if _, err := env.State.Get(baselineKey, &base); err != nil {
		return err
	}
	// count records a total from the game.
	count := func(id string, n int64) {
		if n <= live[id] {
			return
		}
		live[id] = n
		if tracked[id].Kind != xp && base.Day == day && base.Stats[id] == 0 {
			if _, seen := td.Starts[id]; !seen {
				if td.Starts == nil {
					td.Starts = stats{}
				}
				td.Starts[id] = n - 1
			}
		}
	}

	switch d.Type {
	case "KILL_COUNT":
		var x struct {
			Boss  string `json:"boss"`
			Count int64  `json:"count"`
		}
		json.Unmarshal(d.Extra, &x)
		if id, ok := metricFor(x.Boss); ok {
			count(id, x.Count)
		}
	case "LOOT":
		var x struct {
			Source    string     `json:"source"`
			KillCount int64      `json:"killCount"`
			Items     []dinkItem `json:"items"`
		}
		json.Unmarshal(d.Extra, &x)
		id, ok := metricFor(x.Source)
		if ok && x.KillCount > 0 {
			count(id, x.KillCount)
		}
		if dr, big := m.big(x.Items); big {
			dr.Time, dr.Source, dr.KC = now, x.Source, x.KillCount
			td.Drops = append(td.Drops, dr)
			if kc := cmp.Or(x.KillCount, live[id]); ok && kc > 0 {
				lastBig[id] = max(lastBig[id], kc)
			}
		}
	case "CLUE":
		var x struct {
			ClueType        string     `json:"clueType"`
			NumberCompleted int64      `json:"numberCompleted"`
			Items           []dinkItem `json:"items"`
		}
		json.Unmarshal(d.Extra, &x)
		if strings.EqualFold(x.ClueType, "master") {
			count("clue_scrolls_master", x.NumberCompleted)
		}
		if dr, ok := m.big(x.Items); ok {
			dr.Time, dr.Source = now, x.ClueType+" clue"
			td.Drops = append(td.Drops, dr)
			if strings.EqualFold(x.ClueType, "master") {
				lastBig["clue_scrolls_master"] = max(lastBig["clue_scrolls_master"], x.NumberCompleted)
			}
		}
	case "PET":
		var x struct {
			PetName   string `json:"petName"`
			Duplicate bool   `json:"duplicate"`
		}
		json.Unmarshal(d.Extra, &x)
		td.Drops = append(td.Drops, drop{Time: now, Item: x.PetName, Pet: true, Dupe: x.Duplicate})
	case "LOGIN", "XP_MILESTONE":
		var x struct {
			Skills struct {
				Experience map[string]int64 `json:"experience"`
			} `json:"skills"`
			XPData map[string]int64 `json:"xpData"`
		}
		json.Unmarshal(d.Extra, &x)
		for _, xps := range []map[string]int64{x.Skills.Experience, x.XPData} {
			for skill, n := range xps {
				if id := skillMetric(skill); tracked[id].Kind == xp {
					count(id, n)
				}
			}
		}
	default:
		return nil
	}
	if err := env.State.Set(liveKey, live); err != nil {
		return err
	}
	if err := env.State.Set(lastBigKey, lastBig); err != nil {
		return err
	}
	return env.State.Set(todayKey, td)
}

// big is a loot drop worth at least the config's bigLoot, named by its most
// valuable item.
func (m *Monitor) big(items []dinkItem) (drop, bool) {
	var total, best int64
	var dr drop
	for _, it := range items {
		v := it.PriceEach * it.Quantity
		total += v
		if v > best {
			best, dr.Item = v, it.Name
			if it.Quantity > 1 {
				dr.Item += fmt.Sprintf(" ×%d", it.Quantity)
			}
		}
	}
	dr.Value = total
	return dr, total >= m.cfg.BigLoot && dr.Item != ""
}

// drops are today's big drops and pets, newest first, and the events they
// raise. Raising them on every poll is harmless: the engine records an
// event once per key.
func dropsToday(td today, day string) ([]drop, []monitor.Event) {
	if td.Day != day {
		return nil, nil
	}
	var out []drop
	var events []monitor.Event
	for i := len(td.Drops) - 1; i >= 0; i-- {
		dr := td.Drops[i]
		out = append(out, dr)
		ev := monitor.Event{Key: fmt.Sprintf("drop:%d", dr.Time.UnixMilli()), Time: dr.Time, Priority: monitor.PriorityNormal}
		if dr.Pet {
			ev.Key = fmt.Sprintf("pet:%d", dr.Time.UnixMilli())
			ev.Title, ev.Body, ev.Priority = "Pet: "+petName(dr), "You have a funny feeling like you're being followed.", monitor.PriorityHigh
			if dr.Dupe {
				ev.Body = "A duplicate: you already have it."
			}
		} else {
			ev.Title = dr.Item + " · " + gp(dr.Value)
			ev.Body = dropSource(dr)
			ev.URL = wiki(strings.SplitN(dr.Item, " ×", 2)[0])
		}
		events = append(events, ev)
	}
	return out, events
}

func petName(dr drop) string {
	if dr.Item == "" {
		return "a new pet"
	}
	return dr.Item
}

func dropSource(dr drop) string {
	if dr.KC > 0 {
		return fmt.Sprintf("%s, kill %s", dr.Source, comma(dr.KC))
	}
	return dr.Source
}

// dropTable lists today's big drops and pets.
func dropTable(drops []drop, loc *time.Location) monitor.Block {
	b := monitor.Block{Type: monitor.BlockTable, Title: "Big drops", Columns: []monitor.Column{
		{Title: ""}, {Title: "", Fill: true}, {Title: "", Align: monitor.AlignRight}, {Title: ""},
	}}
	for _, dr := range drops {
		row := []monitor.Cell{{Text: dr.Time.In(loc).Format("15:04"), Tone: monitor.ToneMuted}}
		if dr.Pet {
			row = append(row, monitor.Cell{Text: petName(dr), Tone: monitor.ToneAccent}, monitor.Cell{Text: "Pet", Tone: monitor.ToneAccent}, monitor.Cell{})
		} else {
			name := strings.SplitN(dr.Item, " ×", 2)[0]
			row = append(row, monitor.Cell{Text: dr.Item, URL: wiki(name)},
				monitor.Cell{Text: gp(dr.Value), Tone: monitor.ToneGood},
				monitor.Cell{Text: dropSource(dr), Tone: monitor.ToneMuted})
		}
		b.Rows = append(b.Rows, row)
	}
	return b
}

func wiki(item string) string {
	return "https://oldschool.runescape.wiki/w/" + url.PathEscape(strings.ReplaceAll(item, " ", "_"))
}

// gp is an amount of coins in short: "92.8m", "1.25b".
func gp(n int64) string {
	switch {
	case n >= 1e9:
		return trim(float64(n)/1e9, 2) + "b"
	case n >= 1e6:
		return trim(float64(n)/1e6, 1) + "m"
	case n >= 1e3:
		return trim(float64(n)/1e3, 0) + "k"
	}
	return comma(n)
}

func trim(f float64, places int) string {
	s := fmt.Sprintf("%.*f", places, f)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// metricFor is the tracked WOM metric for a boss or activity as the game
// names it, e.g. "Phosani's Nightmare" or "Tombs of Amascut: Expert Mode".
// Skills and bosses without a pet aren't matched.
func metricFor(name string) (string, bool) {
	n := key(name)
	if a, ok := aliases[n]; ok {
		n = a
	}
	for _, id := range []string{n, "the_" + n, strings.TrimPrefix(n, "the_")} {
		if mt, ok := tracked[id]; ok && mt.Kind != xp {
			return id, true
		}
	}
	return "", false
}

// aliases are the game's names that don't become WOM's by key alone.
var aliases = map[string]string{
	"tombs_of_amascut_expert_mode": "tombs_of_amascut_expert",
	"chambers_of_xeric_cm":         "chambers_of_xeric_challenge_mode",
	"gauntlet":                     "the_gauntlet",
	"crystalline_hunllef":          "the_gauntlet",
	"corrupted_hunllef":            "the_corrupted_gauntlet",
	"royal_titan":                  "the_royal_titans",
	"nightmare_of_ashihama":        "nightmare",
	"rewards_guardian":             "guardians_of_the_rift",
	"supply_crate_wintertodt":      "wintertodt",
	"reward_cart_wintertodt":       "wintertodt",
	"reward_pool_tempoross":        "tempoross",
	"fortis_colosseum":             "sol_heredit",
}

// skillMetric is WOM's name for a skill as Dink names it.
func skillMetric(skill string) string {
	if s := key(skill); s != "runecraft" {
		return s
	}
	return "runecrafting"
}

// key lowercases a name into WOM's style: "K'ril Tsutsaroth" is
// "kril_tsutsaroth", "Supply crate (Wintertodt)" "supply_crate_wintertodt".
func key(name string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r == '\'' || r == '’':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if gap && b.Len() > 0 {
				b.WriteByte('_')
			}
			gap = false
			b.WriteRune(r)
		default:
			gap = true
		}
	}
	return b.String()
}

// samePlayer compares OSRS names, where spaces, underscores and hyphens
// are the same and case doesn't matter.
func samePlayer(a, b string) bool {
	norm := func(s string) string {
		return strings.NewReplacer("_", " ", "-", " ", " ", " ").Replace(strings.ToLower(strings.TrimSpace(s)))
	}
	return norm(a) == norm(b)
}
