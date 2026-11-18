package osrs

import (
	"encoding/json"
	"fmt"
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
	by := map[int]int64{}
	for _, r := range rolls {
		if !r.At.Before(midnight) {
			by[r.Chance]++
		}
	}
	g, ok := rollGain(tracked["farming"].Name, tracked["farming"].Pet, "farming", by, "patches")
	if ok && g.Gained == 1 {
		g.Today = "+1 patch"
	}
	return g, ok
}
