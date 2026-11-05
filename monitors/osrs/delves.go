package osrs

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// The Doom of Mokhaiotl is fought in delves, levels 1 to 8 and then on
// past 8, and Dom can drop on finishing any level from 6, at a better rate
// the deeper (1/1,000 at 6 to 1/250 past 8). The hiscores only count
// level 8s, so the counts come from the Delve Calculator plugin, which
// keeps them in RuneLite's profile file as JSON on one line.

// delvePrefix starts the Delve Calculator's line in the profile file.
const delvePrefix = "delvecalculator.killCountData="

// delvesKey holds the last upload's counts.
const delvesKey = "delves"

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

// uploadDelves takes the Delve Calculator's line from the profile file.
// Its STANDARD profile is normal worlds (SEASONAL is leagues; the MANUAL
// ones are counts typed in by hand, left out).
func (m *Monitor) uploadDelves(env monitor.Env, text string) error {
	var line string
	for _, l := range strings.Split(text, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), delvePrefix); ok {
			line = v
		}
	}
	if line == "" {
		return fmt.Errorf("%w: no Delve Calculator data", monitor.ErrBadAction)
	}
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
