package farming

//go:generate go run ./gen -o runelite.go

import (
	"strconv"
	"strings"
	"time"
)

// cropState is what a patch is doing, as RuneLite's CropState.
type cropState int

const (
	growing cropState = iota
	harvestable
	diseased
	dead
	empty
	filling
)

// produce is a crop (RuneLite's Produce): Tick is minutes per growth tick,
// Stages its growth stages; Regrow and Harvests are the same once grown.
type produce struct {
	Name, Impl                     string
	Tick, Stages, Regrow, Harvests int
}

// implementation is a kind of patch (RuneLite's PatchImplementation): what
// each varbit value means. HealthCheck is true for trees and the like,
// which are checked once grown rather than harvested.
type implementation struct {
	Tab, Name   string
	HealthCheck bool
	Rules       []rule
}

// rule reads varbit values Lo to Hi as Produce in State at stage
// Mul*value + Add.
type rule struct {
	Lo, Hi   int
	Produce  string
	State    cropState
	Mul, Add int
}

// region is a place with patches, by RuneLite's map region ID, which with
// a patch's varbit makes the key RuneLite saves it under. Definite says
// it's named with "the", e.g. the Farming Guild.
type region struct {
	Name     string
	ID       int
	Definite bool
	Patches  []patch
}

type patch struct {
	Name   string
	Varbit int
	Impl   string
}

// patchState is a patch's state read from its varbit (RuneLite's
// PatchState).
type patchState struct {
	Produce string
	State   cropState
	Stage   int
}

// forValue reads a varbit value for a kind of patch, or reports false if
// RuneLite doesn't know it.
func (im implementation) forValue(v int) (patchState, bool) {
	for _, r := range im.Rules {
		if v >= r.Lo && v <= r.Hi {
			return patchState{Produce: r.Produce, State: r.State, Stage: r.Mul*v + r.Add}, true
		}
	}
	return patchState{}, false
}

func (s patchState) stages() int {
	p := produces[s.Produce]
	if s.State == harvestable || s.State == filling {
		return p.Harvests
	}
	return p.Stages
}

// tickRate is the minutes between the patch's changes: none unless it's
// growing or regrowing.
func (s patchState) tickRate() int {
	switch s.State {
	case harvestable:
		return produces[s.Produce].Regrow
	case growing:
		return produces[s.Produce].Tick
	}
	return 0
}

// profile is what RuneLite's Time Tracking plugin saved for one account:
// its timetracking config, keyed without the "timetracking.rsprofile.<id>."
// prefix, e.g. "11058.4771" → "226:1788887118".
type profile map[string]string

// prediction is where a patch has got to (RuneLite's PatchPrediction):
// Done is when it finishes growing, zero if it isn't.
type prediction struct {
	Produce string
	State   cropState
	Done    time.Time
	Stage   int
	Stages  int
	// Seen is when RuneLite last saw the patch.
	Seen time.Time
}

// predict ports RuneLite's FarmingTracker.predictPatch: from the value and
// time RuneLite last saw, count the farming ticks since.
func (pr profile) predict(r region, p patch, now time.Time) (prediction, bool) {
	value, at, ok := strings.Cut(pr[strconv.Itoa(r.ID)+"."+strconv.Itoa(p.Varbit)], ":")
	if !ok {
		return prediction{}, false
	}
	v, err1 := strconv.Atoi(value)
	unix, err2 := strconv.ParseInt(at, 10, 64)
	if err1 != nil || err2 != nil || unix <= 0 {
		return prediction{}, false
	}
	state, ok := implementations[p.Impl].forValue(v)
	if !ok {
		return prediction{}, false
	}
	stage, stages, rate := state.Stage, state.stages(), state.tickRate()
	if pr["autoweed"] == "2" && state.Produce == "WEEDS" { // Autoweed.ON
		stage, stages, rate = 0, 1, 0
	}
	out := prediction{Produce: state.Produce, State: state.State, Seen: time.Unix(unix, 0)}
	if rate > 0 {
		tickNow := pr.tickTime(rate, 0, now.Unix())
		tickThen := pr.tickTime(rate, 0, unix)
		stage += int((tickNow - tickThen) / int64(rate*60))
		out.Done = time.Unix(pr.tickTime(rate, stages-1-state.Stage, tickThen), 0)
		stage = min(stage, stages-1)
	}
	out.Stage, out.Stages = stage, stages
	return out, true
}

// tickTime ports FarmingTracker.getTickTime: when the farming tick ticks
// after the one at or before at happens, for crops growing every rate
// minutes. RuneLite learns each account's offset from the clock
// (farmTickOffset, in minutes) by watching patches grow.
func (pr profile) tickTime(rate, ticks int, at int64) int64 {
	var offset int64
	precision, err1 := strconv.Atoi(pr["farmTickOffsetPrecision"])
	mins, err2 := strconv.Atoi(pr["farmTickOffset"])
	if err1 == nil && err2 == nil && (precision >= rate || precision >= 40) {
		offset = int64(mins%rate) * 60
	}
	t := at + offset
	tick := t - t%int64(rate*60)
	return tick + int64(ticks*rate*60) - offset
}
