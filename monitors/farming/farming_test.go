package farming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A teak planted at Anglers' Retreat at planted (value 8 is stage 0 of 8),
// on an account whose farming ticks are 31 minutes off the clock: the
// 640-minute tick before it was at tick, so it's grown 7 ticks later.
const (
	planted int64 = 1792011040
	tick    int64 = 1792010940
	grown   int64 = tick + 7*640*60
)

func teak(extra ...string) profile {
	p := profile{"9770.4771": "8:1792011040", "farmTickOffset": "31", "farmTickOffsetPrecision": "640"}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	return p
}

func anglers() (region, patch) {
	for _, r := range regions {
		if r.Name == "Anglers' Retreat" {
			return r, r.Patches[0]
		}
	}
	panic("no Anglers' Retreat")
}

func TestGeneratedData(t *testing.T) {
	assert.Equal(t, produce{Name: "Teak", Impl: "HARDWOOD_TREE", Tick: 640, Stages: 8, Harvests: 1}, produces["TEAK"])
	s, ok := implementations["HARDWOOD_TREE"].forValue(8)
	require.True(t, ok)
	assert.Equal(t, patchState{Produce: "TEAK", State: growing}, s)
	s, _ = implementations["HARDWOOD_TREE"].forValue(1)
	assert.Equal(t, patchState{Produce: "WEEDS", State: growing, Stage: 2}, s, "weeds count down")
	r, p := anglers()
	assert.Equal(t, 9770, r.ID)
	assert.Equal(t, patch{Varbit: 4771, Impl: "HARDWOOD_TREE"}, p)
	for impl := range implNames {
		assert.Contains(t, implementations, impl)
		assert.Contains(t, patchNames, impl)
	}
	for _, list := range [][]crop{fruitTrees, hardwoods} {
		for _, c := range list {
			assert.Contains(t, produces, c.Produce)
			assert.Contains(t, petBase, c.Produce)
		}
	}
}

func TestPredict(t *testing.T) {
	r, p := anglers()
	pred, ok := teak().predict(r, p, time.Unix(planted+3*640*60, 0))
	require.True(t, ok)
	assert.Equal(t, prediction{Produce: "TEAK", State: growing, Done: time.Unix(grown, 0), Stage: 3, Stages: 8, Seen: time.Unix(planted, 0)}, pred)

	pred, _ = teak().predict(r, p, time.Unix(grown+86400, 0))
	assert.Equal(t, 7, pred.Stage, "the last stage, however long it's been")

	// Without a known offset, ticks fall on the clock's.
	pred, _ = teak("farmTickOffset", "").predict(r, p, time.Unix(planted, 0))
	assert.Equal(t, time.Unix(planted-planted%38400+7*38400, 0), pred.Done)

	_, ok = profile{}.predict(r, p, time.Unix(planted, 0))
	assert.False(t, ok, "never seen")
	_, ok = profile{"9770.4771": "-1:1792011040"}.predict(r, p, time.Unix(planted, 0))
	assert.False(t, ok, "a value RuneLite doesn't know")
}

func TestParseAndPick(t *testing.T) {
	text := strings.Join([]string{
		`timetracking.rsprofile.OLD.9770.4771=8\:1700000000`,
		`timetracking.rsprofile.NEW.9770.4771=8\:1792011040`,
		`timetracking.rsprofile.NEW.farmTickOffset=31`,
		`timetracking.rsprofile.NEW.9770.4771.protected=true`,
		`rsprofile.rsprofile.NEW.displayName=Someone`,
		`junk`,
	}, "\n")
	profiles := parseProperties(text)
	assert.Equal(t, map[string]profile{
		"OLD": {"9770.4771": "8:1700000000"},
		"NEW": {"9770.4771": "8:1792011040", "farmTickOffset": "31", "9770.4771.protected": "true"},
	}, profiles)

	m, err := New(nil)
	require.NoError(t, err)
	assert.Equal(t, profiles["NEW"], m.pick(profiles), "the account last seen farming")
	m.cfg.Profile = "OLD"
	assert.Equal(t, profiles["OLD"], m.pick(profiles))
}

func TestCheapest(t *testing.T) {
	prices := map[int]int{
		5283: 20, 5496: 250, 5986: 80, // apple: seed 20 + 9 × 80 sweetcorn
		5287: 85, 5500: 60, 5982: 6, // pineapple: sapling 60 + 10 × 6 watermelons
	}
	ch, ok := cheapest(fruitTrees, prices, 99, true)
	require.True(t, ok)
	assert.Equal(t, "PINEAPPLE", ch.Produce)
	assert.Equal(t, 120, ch.Total)

	ch, _ = cheapest(fruitTrees, prices, 99, false)
	assert.Equal(t, "APPLE", ch.Produce, "without paying the gardener")
	assert.Equal(t, 20, ch.Total)

	ch, _ = cheapest(fruitTrees, prices, 40, true)
	assert.Equal(t, "APPLE", ch.Produce, "pineapples need 51 Farming")

	_, ok = cheapest(fruitTrees, nil, 99, true)
	assert.False(t, ok, "no prices")
}

func TestStatus(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/London")
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		impl, produce string
		state         cropState
		done          time.Time
		want          string
		ready         bool
	}{
		{"HARDWOOD_TREE", "TEAK", growing, now.Add(time.Hour), "14:00", false},
		{"HARDWOOD_TREE", "TEAK", growing, now.Add(48 * time.Hour), "Sat 13:00", false},
		{"HARDWOOD_TREE", "TEAK", growing, now.Add(-time.Hour), "Check health", true},
		{"HARDWOOD_TREE", "TEAK", harvestable, time.Time{}, "Replant", true},
		{"SEAWEED", "SEAWEED", growing, now.Add(-time.Hour), "Harvest", true},
		{"HESPORI", "HESPORI", growing, now.Add(-time.Hour), "Fight", true},
		{"ANIMA", "KRONOS", growing, now.Add(-time.Hour), "Replant", true},
		{"TREE", "WEEDS", growing, time.Time{}, "Plant", true},
		{"TREE", "MAGIC", diseased, time.Time{}, "Diseased", true},
		{"TREE", "MAGIC", dead, time.Time{}, "Dead", true},
	}
	for _, c := range cases {
		r := row{patch: patch{Impl: c.impl}, pred: prediction{Produce: c.produce, State: c.state, Done: c.done}}
		text, _, ready := r.status(now, loc)
		assert.Equal(t, c.want, text, c.impl+" "+c.produce)
		assert.Equal(t, c.ready, ready, c.impl+" "+c.produce)
	}
}

// fakeAPIs answers for the OSRS Wiki's prices and Wise Old Man.
func fakeAPIs(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.Header.Get("User-Agent"), "kitchensink")
		switch r.URL.Path {
		case "/prices/latest":
			w.Write([]byte(`{"data": {"21486": {"high": 80, "low": 75}, "21477": {"high": 600}, "225": {"high": 170},
				"21488": {"high": 150}, "5998": {"high": 600}}}`))
		case "/wom/players/Some One":
			w.Write([]byte(`{"latestSnapshot": {"data": {"skills": {"farming": {"level": 99}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPoll(t *testing.T) {
	srv := fakeAPIs(t)
	m, err := New(json.RawMessage(`{"player": "Some One", "pricesURL": "` + srv.URL + `/prices", "womURL": "` + srv.URL + `/wom"}`))
	require.NoError(t, err)
	now := time.Unix(planted+3*640*60, 0)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, &monitor.Upload{Path: defaultFile, Prefixes: []string{prefix}}, res.Upload)
	require.Len(t, res.Blocks, 1)
	assert.Contains(t, res.Blocks[0].Text, "Waiting")

	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "upload", Text: "nothing"}), monitor.ErrBadAction)
	upload := "timetracking.rsprofile.A.9770.4771=8\\:1792011040\n" + // Anglers' Retreat: teak
		"timetracking.rsprofile.A.14651.4771=3\\:1792011040\n" + // Fossil Island east: weeds
		"timetracking.rsprofile.A.11058.4771=0\\:1792011040\n" + // Brimhaven fruit tree: weeds
		"timetracking.rsprofile.A.farmTickOffset=31\ntimetracking.rsprofile.A.farmTickOffsetPrecision=640\n"
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "upload", Text: upload}))

	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "Farming 99 · last seen in game 14 Oct 21:50", res.Subtitle)
	require.Len(t, res.Blocks, 2)
	fruit, hard := res.Blocks[0], res.Blocks[1]
	assert.Equal(t, "Fruit trees", fruit.Title, "no fruit tree prices")
	assert.Equal(t, "Hardwood · cheapest is Teak, 2.6k a patch", hard.Title, "seed 80 + 15 × 170 limpwurt")
	require.Len(t, hard.Rows, 2)
	assert.Equal(t, []monitor.Cell{{Text: "Anglers' Retreat"}, {Text: "Teak"}, {Text: "1/2,525", Tone: monitor.ToneMuted}, {Text: "Sun 00:29"}}, hard.Rows[0])
	assert.Equal(t, []monitor.Cell{{Text: "Fossil Island east"}, {Text: "Hardwood patch", Tone: monitor.ToneMuted}, {Tone: monitor.ToneMuted}, {Text: "Plant", Tone: monitor.ToneAccent}}, hard.Rows[1])
	assert.Equal(t, []monitor.Stat{
		{Label: "Fruit", Value: "Ready", Tone: monitor.ToneGood},
		{Label: "Hardwood", Value: "Sun 00:29", Detail: "1 of 2 ready", Tone: monitor.ToneAccent},
	}, res.Summary)
	assert.Empty(t, res.Events)
	assert.Equal(t, time.Unix(grown, 0).Sub(now)+30*time.Second, res.Next, "polls when the teak's grown")

	// Grown: one event for the hardwoods, keyed by when.
	env.Now = func() time.Time { return time.Unix(grown+60, 0) }
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "ready:HARDWOOD_TREE:1792279740", res.Events[0].Key)
	assert.Equal(t, "Hardwoods ready", res.Events[0].Title)
	assert.Equal(t, "Anglers' Retreat, Fossil Island east", res.Events[0].Body)
	assert.Equal(t, monitor.Stat{Label: "Hardwood", Value: "Ready", Tone: monitor.ToneGood}, res.Summary[1])

	// Long after, it's no longer news.
	env.Now = func() time.Time { return time.Unix(grown+13*3600, 0) }
	res, _ = m.Poll(context.Background(), env)
	assert.Empty(t, res.Events)
}

func TestGP(t *testing.T) {
	assert.Equal(t, "950", gp(950))
	assert.Equal(t, "2.6k", gp(2630))
	assert.Equal(t, "2k", gp(2010))
	assert.Equal(t, "12k", gp(12_500))
	assert.Equal(t, "3.2m", gp(3_200_000))
	assert.Equal(t, "1,234,567", thousands(1234567))
}
