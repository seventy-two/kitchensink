package osrs

import (
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
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Quantity  int64  `json:"quantity"`
	PriceEach int64  `json:"priceEach"`
	// Rarity is the item's drop chance (0.001 for 1/1,000), from the wiki,
	// for NPC drops only; zero when Dink doesn't know it.
	Rarity float64 `json:"rarity"`
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
	// These are kept for the history: the most valuable item's ID and drop
	// chance, the tracked metric the drop came from, and whether KC is an
	// estimate (Loot Logger doesn't count raids, so imports number them).
	ItemID   int     `json:"id,omitempty"`
	Rarity   float64 `json:"rarity,omitempty"`
	Metric   string  `json:"metric,omitempty"`
	ApproxKC bool    `json:"approxKC,omitempty"`
	// Delves is, for a Doom of Mokhaiotl drop, how many delve levels of 6
	// or deeper (those that can give Dom) the player had done, from the
	// last Delve Calculator upload.
	Delves int64 `json:"delves,omitempty"`
}

// today is what Dink has told the monitor today, beyond counts.
type today struct {
	Day   string `json:"day"`
	Drops []drop `json:"drops,omitempty"`
	// Starts are where the day began by Dink's counts: everything it had
	// counted before its first message after midnight, which beats the
	// hiscores' baseline when a session runs past midnight (they only
	// update at logout). For kill counts the hiscores don't rank yet, also
	// the count before the first kill Dink saw today.
	Starts stats `json:"starts,omitempty"`
}

const (
	liveKey  = "live"
	todayKey = "today"
	// lastBigKey holds, by metric, the kill count of the last drop worth
	// bigLoot or pet: from Dink, or set with a "lastBig" action (e.g. from
	// RuneLite's Loot Logger history).
	lastBigKey = "lastBig"
	// lastKillKey is the metric Dink last counted a kill for, which a pet
	// is put down to: Dink doesn't say where a pet came from.
	lastKillKey = "lastKill"
	// seenKey is when Dink last sent something from the player, i.e.
	// they're playing.
	seenKey = "seen"
	// ownedKey lists the pets Dink has reported, beyond the config's.
	ownedKey = "owned"
	// historyKey is every drop worth historyLoot and every pet, oldest
	// first, kept for good: from Dink, or imported with "importDrops".
	historyKey = "history"
)

// owned is every pet the player has, by petKey: the config's and Dink's.
func (m *Monitor) owned(env monitor.Env) (map[string]bool, error) {
	var got []string
	if _, err := env.State.Get(ownedKey, &got); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range append(got, m.cfg.Owned...) {
		out[petKey(name)] = true
	}
	return out, nil
}

// playing is how long after Dink's last message the panel counts as
// relevant.
const playing = 30 * time.Minute

// lastKill is the last kill Dink reported.
type lastKill struct {
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
}

// HookSecret is the secret Dink's webhook URL carries.
func (m *Monitor) HookSecret() string { return m.cfg.HookSecret }

// Act takes Dink's notifications. Other players' (an alt on the same
// RuneLite) and those from leagues or Deadman are ignored, as are kinds
// the panel doesn't use.
//
// A "lastBig" action replaces the kill counts of the last big drops by
// metric, as data {"vorkath": 1020}.
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
		for id := range set {
			if mt, ok := tracked[id]; !ok || mt.Kind == xp {
				return fmt.Errorf("%w: no boss %q", monitor.ErrBadAction, id)
			}
		}
		return env.State.Set(lastBigKey, set)
	}
	if a.Type == "importDrops" {
		return m.importDrops(env, a.Data)
	}
	if a.Type == monitor.UploadAction {
		return m.uploadDelves(env, a.Text)
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
		td = today{Day: day, Starts: merged(live, nil)}
	}
	var base baseline
	if _, err := env.State.Get(baselineKey, &base); err != nil {
		return err
	}
	var last lastKill
	if _, err := env.State.Get(lastKillKey, &last); err != nil {
		return err
	}
	var history []drop
	if _, err := env.State.Get(historyKey, &history); err != nil {
		return err
	}
	killed := func(id string) { last = lastKill{ID: id, Time: now} }
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
			killed(id)
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
		if ok {
			killed(id)
		}
		dr := m.top(x.Items)
		dr.Time, dr.Source, dr.KC = now, x.Source, x.KillCount
		if ok {
			dr.Metric = id
			if dr.KC == 0 {
				dr.KC = live[id]
			}
		}
		if id == "doom_of_mokhaiotl" {
			var dv delves
			if _, err := env.State.Get(delvesKey, &dv); err != nil {
				return err
			}
			dr.Delves = dv.domLevels()
		}
		if dr.Value >= m.cfg.BigLoot {
			td.Drops = append(td.Drops, dr)
			if ok && dr.KC > 0 {
				lastBig[id] = max(lastBig[id], dr.KC)
			}
		}
		if dr.Value >= m.cfg.HistoryLoot {
			history = append(history, dr)
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
		dr := m.top(x.Items)
		dr.Time, dr.Source = now, x.ClueType+" clue"
		master := strings.EqualFold(x.ClueType, "master")
		if master {
			dr.Metric, dr.KC = "clue_scrolls_master", x.NumberCompleted
		}
		if dr.Value >= m.cfg.BigLoot {
			td.Drops = append(td.Drops, dr)
			if master {
				lastBig["clue_scrolls_master"] = max(lastBig["clue_scrolls_master"], x.NumberCompleted)
			}
		}
		if dr.Value >= m.cfg.HistoryLoot {
			history = append(history, dr)
		}
	case "PET":
		var x struct {
			PetName   string `json:"petName"`
			Duplicate bool   `json:"duplicate"`
		}
		json.Unmarshal(d.Extra, &x)
		dr := drop{Time: now, Item: x.PetName, Pet: true, Dupe: x.Duplicate}
		// Put down to the kill just before, if that boss gives this pet.
		if mt, ok := tracked[last.ID]; ok && now.Sub(last.Time) < 10*time.Minute &&
			(x.PetName == "" || strings.EqualFold(x.PetName, mt.Pet)) {
			dr.Source, dr.KC, dr.Metric = mt.Name, live[last.ID], last.ID
			lastBig[last.ID] = max(lastBig[last.ID], live[last.ID])
		}
		td.Drops = append(td.Drops, dr)
		history = append(history, dr)
		if x.PetName != "" && !x.Duplicate {
			var got []string
			if _, err := env.State.Get(ownedKey, &got); err != nil {
				return err
			}
			if err := env.State.Set(ownedKey, append(got, x.PetName)); err != nil {
				return err
			}
		}
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
	if err := env.State.Set(seenKey, now); err != nil {
		return err
	}
	if err := env.State.Set(liveKey, live); err != nil {
		return err
	}
	if err := env.State.Set(lastBigKey, lastBig); err != nil {
		return err
	}
	if err := env.State.Set(lastKillKey, last); err != nil {
		return err
	}
	if err := env.State.Set(historyKey, history); err != nil {
		return err
	}
	return env.State.Set(todayKey, td)
}

// top is a loot drop's total value, named by its most valuable item.
func (m *Monitor) top(items []dinkItem) drop {
	var best int64
	var dr drop
	for _, it := range items {
		v := it.PriceEach * it.Quantity
		dr.Value += v
		if v > best {
			best, dr.Item, dr.ItemID, dr.Rarity = v, it.Name, it.ID, it.Rarity
			if it.Quantity > 1 {
				dr.Item += fmt.Sprintf(" ×%d", it.Quantity)
			}
		}
	}
	if dr.Item == "" {
		dr.Value = 0 // nothing worth anything
	}
	return dr
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
			if dr.Source != "" {
				ev.Body = dropSource(dr)
			}
			if dr.Dupe {
				ev.Body = strings.TrimPrefix(dropSource(dr)+". ", ". ") + "A duplicate: you already have it."
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
			row = append(row, monitor.Cell{Text: petName(dr), Tone: monitor.ToneAccent}, monitor.Cell{Text: "Pet", Tone: monitor.ToneAccent},
				monitor.Cell{Text: dropSource(dr), Tone: monitor.ToneMuted})
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
