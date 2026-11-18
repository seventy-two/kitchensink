package farming

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Roll is a Tangleroot roll RuneLite's data shows: a tree's health
// checked, or a crop harvested and replanted. Chance is "1 in Chance" at
// the player's Farming level.
type Roll struct {
	At     time.Time `json:"at"`
	Patch  string    `json:"patch"` // region ID and varbit, e.g. "9770.4771"
	Crop   string    `json:"crop"`  // e.g. "Maple"
	Chance int       `json:"chance"`
}

// rolls is the monitor's memory of rolls: the last value RuneLite saved
// for each patch, so a value seen again (passing a checked tree) isn't
// another roll, and the rolls of the last two days.
type rolls struct {
	Last  map[string]string `json:"last"`
	Rolls []Roll            `json:"rolls"`
}

const rollsKey = "rolls"

// keepRolls is how long rolls are kept: long enough for "today" in any
// time zone.
const keepRolls = 48 * time.Hour

// OSRSAction is the action the farming monitor sends the osrs monitor
// with the rolls it knows, after each change; its Data is a []Roll.
const OSRSAction = "farming"

// evidence reports whether a patch's saved state shows a roll, and the
// crop it was for: a crop with a health check seen checked (harvestable,
// straight after growing, not a stump after it), or any crop seen just
// planted (growing at stage 0), which follows a check or the final
// harvest of the crop before. prev is the state saved before, if known.
func evidence(p patch, s patchState, prev *patchState) (string, bool) {
	if petBase[s.Produce] == 0 {
		return "", false
	}
	switch {
	case s.State == harvestable && implementations[p.Impl].HealthCheck:
		return s.Produce, prev == nil || prev.State != harvestable
	case s.State == growing && s.Stage == 0:
		return s.Produce, true
	}
	return "", false
}

// record adds the rolls the profile shows since the last upload. A patch
// counts once a crop: a check then a replant minutes later is one roll,
// as is anything within half the crop's growing time.
func (rs *rolls) record(pr profile, level int, now time.Time) bool {
	if rs.Last == nil {
		rs.Last = map[string]string{}
	}
	changed := false
	for _, r := range regions {
		for _, p := range r.Patches {
			if runOf(p.Impl) < 0 {
				continue
			}
			key := strconv.Itoa(r.ID) + "." + strconv.Itoa(p.Varbit)
			saved, ok := pr[key]
			if !ok || rs.Last[key] == saved {
				continue
			}
			prev := readState(p, rs.Last[key])
			rs.Last[key], changed = saved, true
			value, at, _ := strings.Cut(saved, ":")
			v, err1 := strconv.Atoi(value)
			unix, err2 := strconv.ParseInt(at, 10, 64)
			s, ok := implementations[p.Impl].forValue(v)
			if err1 != nil || err2 != nil || !ok {
				continue
			}
			crop, ok := evidence(p, s, prev)
			seen := time.Unix(unix, 0).UTC()
			if !ok || now.Sub(seen) > keepRolls {
				continue
			}
			pd := produces[crop]
			gap := time.Duration(pd.Tick*pd.Stages) * time.Minute / 2
			if last := rs.last(key); !last.IsZero() && seen.Sub(last) < gap {
				continue
			}
			name := pd.Name
			if name == "" {
				name = crop
			}
			rs.Rolls = append(rs.Rolls, Roll{At: seen, Patch: key, Crop: name, Chance: petChance(crop, max(level, 1))})
		}
	}
	kept := rs.Rolls[:0]
	for _, r := range rs.Rolls {
		if now.Sub(r.At) <= keepRolls {
			kept = append(kept, r)
		}
	}
	rs.Rolls = kept
	return changed
}

// readState reads a saved "value:time", or nil.
func readState(p patch, saved string) *patchState {
	value, _, _ := strings.Cut(saved, ":")
	v, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	s, ok := implementations[p.Impl].forValue(v)
	if !ok {
		return nil
	}
	return &s
}

// last is when the patch last rolled, or zero.
func (rs *rolls) last(key string) time.Time {
	var t time.Time
	for _, r := range rs.Rolls {
		if r.Patch == key && r.At.After(t) {
			t = r.At
		}
	}
	return t
}

// sendRolls records the profile's rolls and, when that changes them,
// sends them to the osrs monitor (if there is one) for its day's gains.
// Failures are only logged: the farming panel doesn't need them.
func (m *Monitor) sendRolls(ctx context.Context, env monitor.Env, pr profile) {
	var rs rolls
	if _, err := env.State.Get(rollsKey, &rs); err != nil {
		env.Log.Warn("reading rolls", "err", err)
		return
	}
	before, _ := json.Marshal(rs.Rolls)
	if !rs.record(pr, m.level, env.Now()) {
		return
	}
	if err := env.State.Set(rollsKey, rs); err != nil {
		env.Log.Warn("saving rolls", "err", err)
		return
	}
	data, _ := json.Marshal(rs.Rolls)
	if string(data) == string(before) || m.cfg.OSRS == "" || env.Act == nil {
		return
	}
	if err := env.Act(ctx, m.cfg.OSRS, monitor.Action{Type: OSRSAction, Data: data}); err != nil {
		env.Log.Warn("sending rolls to osrs", "err", err)
	}
}
