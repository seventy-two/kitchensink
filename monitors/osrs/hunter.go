package osrs

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Hunter's pets come from catching particular creatures, not from Hunter
// XP: the baby chinchompa from chinchompas, Mr McGroot from Wyrmscraig's
// goats. So the panel counts catches, each kind at its own rate (1 in
// base - 25 × Hunter level, from the wiki), in place of Hunter XP.
//
// Chinchompas come from Dink's chat notifier, set to send the game's
// "You've caught a ... chinchompa." messages; goats from the goat
// hunting plugin's count in the profile file (see plugins.go).

// chinBase is each chinchompa's base chance of the baby chinchompa.
var chinBase = map[string]int{"grey": 131_395, "red": 98_373, "black": 82_758}

// goatBase is a goat's base chance of Mr McGroot.
const goatBase = 40_000

// catches are the day's chinchompas caught, by kind.
type catches struct {
	Day    string           `json:"day"`
	Counts map[string]int64 `json:"counts"`
}

const catchesKey = "catches"

// goatsDay is the goats caught at the day's start.
type goatsDay struct {
	Day   string `json:"day"`
	Start int64  `json:"start"`
}

const goatsDayKey = "goatsDay"

// chatEvery is how often chat messages (a catch every few seconds) have
// the panel refresh, at most; Poll catches up with the rest.
const chatEvery = time.Minute

// chinKind is the kind of chinchompa a game message says was caught, or
// "" if it isn't one.
func chinKind(msg string) string {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "caught") || !strings.Contains(m, "chinchompa") {
		return ""
	}
	switch {
	case strings.Contains(m, "black"):
		return "black"
	case strings.Contains(m, "carnivorous"), strings.Contains(m, "red"):
		return "red"
	}
	return "grey"
}

// chat takes a message from Dink's chat notifier: a chinchompa caught is
// counted; anything else is logged (to see what the notifier sends) and
// ignored.
func (m *Monitor) chat(env monitor.Env, d dink, day string) error {
	var x struct {
		Message string `json:"message"`
	}
	json.Unmarshal(d.Extra, &x)
	kind := chinKind(x.Message)
	if kind == "" {
		if env.Log != nil {
			env.Log.Info("osrs: chat message not counted", "message", x.Message)
		}
		return monitor.ErrQuiet
	}
	var c catches
	if _, err := env.State.Get(catchesKey, &c); err != nil {
		return err
	}
	if c.Day != day || c.Counts == nil {
		c = catches{Day: day, Counts: map[string]int64{}}
	}
	c.Counts[kind]++
	if err := env.State.Set(catchesKey, c); err != nil {
		return err
	}
	now := env.Now()
	if err := env.State.Set(seenKey, now); err != nil {
		return err
	}
	if now.Sub(m.chatAt) < chatEvery {
		m.pending = true
		return monitor.ErrQuiet
	}
	m.chatAt = now
	return nil
}

// countGoats keeps the day's starting count when the goat plugin's total
// arrives: the last count before today's first.
func (m *Monitor) countGoats(env monitor.Env, goats int64) error {
	day := env.Now().In(m.loc).Format(time.DateOnly)
	var gd goatsDay
	if _, err := env.State.Get(goatsDayKey, &gd); err != nil {
		return err
	}
	if gd.Day == day {
		return nil
	}
	var before int64
	ok, err := env.State.Get(goatsKey, &before)
	if err != nil {
		return err
	}
	if !ok || gd.Day == "" {
		// The first since goats counted by day: the count before may be a
		// day old (it was uploaded daily), so today starts now.
		before = goats
	}
	return env.State.Set(goatsDayKey, goatsDay{Day: day, Start: min(before, goats)})
}

// hunterGains are the day's chinchompa and goat rows, at the Hunter level
// the XP gives.
func (m *Monitor) hunterGains(env monitor.Env, day string, hunterXP int64) ([]gain, error) {
	level := levelFor(hunterXP)
	var out []gain
	var c catches
	if _, err := env.State.Get(catchesKey, &c); err != nil {
		return nil, err
	}
	if c.Day == day {
		rolls := map[int]int64{}
		for kind, n := range c.Counts {
			rolls[chinBase[kind]-25*level] += n
		}
		if g, ok := rollGain("Chinchompas", "Baby chinchompa", "chinchompas", rolls, "caught"); ok {
			out = append(out, g)
		}
	}
	var gd goatsDay
	var goats int64
	if _, err := env.State.Get(goatsDayKey, &gd); err != nil {
		return nil, err
	}
	if _, err := env.State.Get(goatsKey, &goats); err != nil {
		return nil, err
	}
	if gd.Day == day {
		if g, ok := rollGain("Goats", "Mr McGroot", "goats", map[int]int64{goatBase - 25*level: goats - gd.Start}, "caught"); ok {
			out = append(out, g)
		}
	}
	return out, nil
}

// rollGain is a row for rolls at a pet, by "1 in N" chance: how many and
// the chance at least one gave it.
func rollGain(name, pet, id string, rolls map[int]int64, unit string) (gain, bool) {
	none, n := 1.0, int64(0)
	for chance, count := range rolls {
		if chance <= 0 || count <= 0 {
			continue
		}
		none *= math.Pow(1-1/float64(chance), float64(count))
		n += count
	}
	if n == 0 {
		return gain{}, false
	}
	mt := metric{Name: name, Pet: pet, Kind: score, Rate: "1/" + comma(int64(math.Round(1/(1-none))))}
	return gain{metric: mt, ID: id, Gained: n, Today: "+" + comma(n) + " " + unit}, true
}

// levelFor is the level a skill's XP gives (1 to 99).
func levelFor(x int64) int {
	var points float64
	for level := 1; level < 99; level++ {
		points += math.Floor(float64(level) + 300*math.Pow(2, float64(level)/7))
		if x < int64(points/4) {
			return level
		}
	}
	return 99
}
