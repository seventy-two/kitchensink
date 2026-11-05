package osrs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Counts kept by RuneLite plugins in the profile file, for pets whose
// chance the hiscores can't give.
//
// The Doom of Mokhaiotl is fought in delves, levels 1 to 8 and then on
// past 8, and Dom can drop on finishing any level from 6, at a better rate
// the deeper (1/1,000 at 6 to 1/250 past 8). The hiscores only count
// level 8s, so the counts come from the Delve Calculator plugin, which
// keeps them in RuneLite's profile file as JSON on one line.

// delvePrefix starts the Delve Calculator's line in the profile file.
const delvePrefix = "delvecalculator.killCountData="

// delvesKey holds the last upload's counts.
const delvesKey = "delves"

// Mr McGroot comes from hunting goats on Wyrmscraig, a roll per goat
// caught; the goat hunting plugin counts them.
const (
	goatPrefix = "goatindicators.totalCaught="
	// goatsKey holds the last upload's goats caught.
	goatsKey = "goats"
	// goatsStat is the published name for goats caught.
	goatsStat = "goats_caught"
)

// delves are the levels completed, by level (9 is every level past 8).
type delves struct {
	At     time.Time       `json:"at"`
	Levels map[int64]int64 `json:"levels"`
}

// domLevels is how many levels that can give Dom have been done.
func (d delves) domLevels() int64 {
	var n int64
	for level, count := range d.Levels {
		if level >= 6 {
			n += count
		}
	}
	return n
}

// stats are the counts by published name, "doom_delve_6" to "doom_delve_9".
func (d delves) stats() stats {
	out := stats{}
	for level, count := range d.Levels {
		if level >= 6 {
			out[fmt.Sprintf("doom_delve_%d", level)] = count
		}
	}
	return out
}

// uploadProfile takes the plugins' lines from the profile file: either
// can be missing (a plugin not installed), but not both.
func (m *Monitor) uploadProfile(env monitor.Env, text string) error {
	var delveLine, goatLine string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, delvePrefix); ok {
			delveLine = v
		}
		if v, ok := strings.CutPrefix(l, goatPrefix); ok {
			goatLine = v
		}
	}
	if delveLine == "" && goatLine == "" {
		return fmt.Errorf("%w: no plugin counts", monitor.ErrBadAction)
	}
	if goatLine != "" {
		goats, err := strconv.ParseInt(strings.TrimSpace(goatLine), 10, 64)
		if err != nil {
			return fmt.Errorf("%w: goats caught: %v", monitor.ErrBadAction, err)
		}
		if err := env.State.Set(goatsKey, goats); err != nil {
			return err
		}
	}
	if delveLine == "" {
		return nil
	}
	return m.uploadDelves(env, delveLine)
}

// uploadDelves takes the Delve Calculator's value from the profile file.
// Its STANDARD profile is normal worlds (SEASONAL is leagues; the MANUAL
// ones are counts typed in by hand, left out).
func (m *Monitor) uploadDelves(env monitor.Env, line string) error {
	var raw struct {
		Profiles map[string]struct {
			LevelKills map[string]int64 `json:"levelKills"`
			WavesPast8 int64            `json:"wavesPast8"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(unescapeProperty(line)), &raw); err != nil {
		return fmt.Errorf("%w: Delve Calculator: %v", monitor.ErrBadAction, err)
	}
	standard, ok := raw.Profiles["STANDARD"]
	if !ok {
		return fmt.Errorf("%w: no STANDARD Delve Calculator profile", monitor.ErrBadAction)
	}
	d := delves{At: env.Now(), Levels: map[int64]int64{9: standard.WavesPast8}}
	for level, count := range standard.LevelKills {
		n, err := strconv.ParseInt(level, 10, 64)
		if err != nil || n < 1 || n > 8 {
			continue
		}
		d.Levels[n] = count
	}
	return env.State.Set(delvesKey, d)
}

// unescapeProperty undoes a Java properties file's escapes in a value
// ("\:" for ":", "\\" for "\", "\uXXXX").
func unescapeProperty(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 < len(s) {
				if r, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(r))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
