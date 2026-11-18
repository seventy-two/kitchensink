package osrs

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitors/farming"
)

// farmingKey holds the Tangleroot rolls the farming monitor last sent.
const farmingKey = "farming"

// takeRolls keeps the rolls the farming monitor sends (see
// farming.OSRSAction), quietly when they're the same.
func (m *Monitor) takeRolls(env monitor.Env, data json.RawMessage) error {
	var rolls []farming.Roll
	if err := json.Unmarshal(data, &rolls); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	var old []farming.Roll
	if _, err := env.State.Get(farmingKey, &old); err != nil {
		return err
	}
	if slices.EqualFunc(old, rolls, func(a, b farming.Roll) bool { return a.At.Equal(b.At) && a.Patch == b.Patch }) {
		return monitor.ErrQuiet
	}
	return env.State.Set(farmingKey, rolls)
}

// farmingGain is the day's Farming row: the patches checked (or
// harvested) since midnight and the chance that at least one gave
// Tangleroot, from each crop's own rate. Farming XP isn't used: checks
// are what roll for the pet, and the hiscores only have XP after logout.
func farmingGain(rolls []farming.Roll, midnight time.Time) (gain, bool) {
	none := 1.0 // the chance none of them gave it
	n := 0
	for _, r := range rolls {
		if r.At.Before(midnight) || r.Chance <= 0 {
			continue
		}
		none *= 1 - 1/float64(r.Chance)
		n++
	}
	if n == 0 {
		return gain{}, false
	}
	mt := tracked["farming"]
	mt.Kind = score
	mt.Rate = "1/" + comma(int64(math.Round(1/(1-none))))
	unit := " patches"
	if n == 1 {
		unit = " patch"
	}
	return gain{metric: mt, ID: "farming", Gained: int64(n), Today: "+" + comma(int64(n)) + unit}, true
}
