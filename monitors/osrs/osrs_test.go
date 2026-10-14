package osrs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 20:00 in the UK on 14 October.
var now = time.Date(2026, 10, 14, 19, 0, 0, 0, time.UTC)

func snap(skills, bosses, activities map[string]int64) map[string]any {
	data := map[string]any{"skills": map[string]any{}, "bosses": map[string]any{}, "activities": map[string]any{}}
	for k, v := range skills {
		data["skills"].(map[string]any)[k] = map[string]any{"experience": v}
	}
	for k, v := range bosses {
		data["bosses"].(map[string]any)[k] = map[string]any{"kills": v}
	}
	for k, v := range activities {
		data["activities"].(map[string]any)[k] = map[string]any{"score": v}
	}
	return map[string]any{"data": data}
}

// fakeWOM has the player at the start of the day (the last snapshot
// before midnight) and now: 12 Vorkath kills, a first Scurrius kill
// (unranked before), 2 master clues, 150k mining XP, and things that don't
// count towards a pet (attack XP, Barrows chests).
type fakeWOM struct {
	*httptest.Server
	mu        sync.Mutex
	limited   bool // the update is refused as too soon
	requests  []string
	snapQuery string
}

func newFakeWOM(t *testing.T) *fakeWOM {
	f := &fakeWOM{}
	start := snap(map[string]int64{"mining": 13_100_000, "attack": 4_000_000},
		map[string]int64{"vorkath": 100, "scurrius": -1, "barrows_chests": 50},
		map[string]int64{"clue_scrolls_master": 40})
	latest := snap(map[string]int64{"mining": 13_250_000, "attack": 5_000_000},
		map[string]int64{"vorkath": 112, "scurrius": 1, "barrows_chests": 60},
		map[string]int64{"clue_scrolls_master": 42})
	js := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		assert.Contains(t, r.UserAgent(), "kitchensink")
		switch {
		case r.URL.Path == "/players/GTX 480" && r.Method == http.MethodPost && f.limited:
			w.WriteHeader(http.StatusTooManyRequests)
			js(w, map[string]any{"code": "PLAYER_IS_RATE_LIMITED"})
		case r.URL.Path == "/players/GTX 480":
			js(w, map[string]any{"latestSnapshot": latest})
		case r.URL.Path == "/players/GTX 480/snapshots":
			f.snapQuery = r.URL.RawQuery
			js(w, []any{start})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeWOM) count(req string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == req {
			n++
		}
	}
	return n
}

func newMonitor(t *testing.T, f *fakeWOM) *Monitor {
	raw, _ := json.Marshal(map[string]any{"player": "GTX 480", "womURL": f.URL, "hookSecret": "s3cret"})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestToday(t *testing.T) {
	f := newFakeWOM(t)
	m := newMonitor(t, f)
	env := monitortest.Env(now)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	assert.Equal(t, 1, f.count("POST /players/GTX 480"), "asks WOM to update")
	assert.Contains(t, f.snapQuery, "endDate=2026-10-13T23%3A05%3A00Z", "the last snapshot before UK midnight")
	assert.Contains(t, f.snapQuery, "limit=1")
	assert.Empty(t, res.Subtitle)
	require.NotNil(t, res.SummaryBlock)
	assert.Len(t, res.SummaryBlock.Rows, 3, "short, the top three rows of the table")
	assert.Equal(t, res.Blocks[0].Rows[:3], res.SummaryBlock.Rows)
	assert.Len(t, res.Blocks[0].Rows, 4, "the table itself is whole")
	assert.Equal(t, []monitor.Stat{
		{Label: "Vorkath", Value: "+12", Tone: monitor.ToneGood},
		{Label: "Master clues", Value: "+2", Tone: monitor.ToneGood},
		{Label: "Scurrius", Value: "+1", Tone: monitor.ToneGood},
		{Label: "Mining", Value: "+150.0k xp", Tone: monitor.ToneGood},
	}, res.Summary, "kills first, then XP; no attack XP or Barrows")
	require.Len(t, res.Blocks, 1)
	assert.Equal(t, []monitor.Cell{
		{Text: "Vorkath"}, {Text: "+12", Tone: monitor.ToneGood}, {Text: "112", Tone: monitor.ToneMuted},
		{Text: "1/3,000", Tone: monitor.ToneMuted}, {Text: "", Tone: monitor.ToneMuted},
	}, res.Blocks[0].Rows[0])
	assert.Equal(t, "13.25M xp", res.Blocks[0].Rows[3][2].Text)
	assert.Equal(t, 4*time.Hour+time.Minute, res.Next, "wakes just after midnight to mark the new day")

	// A poll soon after (as after a hook) doesn't ask WOM again.
	env.Now = func() time.Time { return now.Add(50 * time.Minute) }
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, f.count("POST /players/GTX 480"))

	// Later the same day: WOM refuses an update so soon, so the latest
	// snapshot does; the day's start is kept.
	f.mu.Lock()
	f.limited = true
	f.mu.Unlock()
	env.Now = func() time.Time { return now.Add(time.Hour) }
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Summary, 4)
	assert.Equal(t, 1, f.count("GET /players/GTX 480"))
	assert.Equal(t, 1, f.count("GET /players/GTX 480/snapshots"), "the day's start is read once")

	// A new day reads its start again.
	env.Now = func() time.Time { return now.Add(5 * time.Hour) }
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 2, f.count("GET /players/GTX 480/snapshots"))
}

func TestNothingYet(t *testing.T) {
	assert.Equal(t, []monitor.Stat{{Label: "Today", Value: "Nothing yet", Tone: monitor.ToneMuted}}, summary(nil, false))
	assert.Empty(t, summary(nil, true), "drops show instead")
	assert.Equal(t, "Nothing towards a pet yet today.", table(nil, nil, 0).Text)
	assert.Empty(t, gains(stats{"attack": 1, "vorkath": 5}, stats{"attack": 9, "vorkath": 5}))
}

func TestAmount(t *testing.T) {
	assert.Equal(t, "12", amount(kills, 12))
	assert.Equal(t, "1,234", amount(kills, 1234))
	assert.Equal(t, "9,999 xp", amount(xp, 9999))
	assert.Equal(t, "85.2k xp", amount(xp, 85_200))
	assert.Equal(t, "13.25M xp", amount(xp, 13_250_000))
}

func TestTrackedNames(t *testing.T) {
	for id, mt := range tracked {
		assert.NotEmpty(t, mt.Name, id)
		assert.NotEmpty(t, mt.Pet, id)
		assert.Equal(t, strings.ToLower(id), id)
	}
	for _, none := range []string{"attack", "barrows_chests", "mimic", "obor", "bryophyta", "hespori", "lunar_chests", "firemaking"} {
		assert.NotContains(t, tracked, none)
	}
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{}`))
	assert.Error(t, err)
}

// hook sends a Dink notification of the given type.
func hook(t *testing.T, m *Monitor, env monitor.Env, typ string, extra string, more ...string) {
	t.Helper()
	body := `{"type":"` + typ + `","playerName":"GTX 480","accountType":"NORMAL","seasonalWorld":false,"extra":` + extra + strings.Join(more, "") + `}`
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: monitor.HookAction, Data: json.RawMessage(body)}))
}

func TestDink(t *testing.T) {
	f := newFakeWOM(t)
	m := newMonitor(t, f)
	assert.Equal(t, "s3cret", m.HookSecret())
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	later := now.Add(time.Minute)
	env.Now = func() time.Time { return later }
	// The last big drops from before Dink, e.g. from Loot Logger.
	lastBig := func(data string) error {
		return m.Act(context.Background(), env, monitor.Action{Type: "lastBig", Data: json.RawMessage(data)})
	}
	require.NoError(t, lastBig(`{"vorkath":110,"phosanis_nightmare":380}`))
	assert.ErrorIs(t, lastBig(`{"mining":1}`), monitor.ErrBadAction)
	assert.ErrorIs(t, lastBig(`{"barrows_chests":1}`), monitor.ErrBadAction)

	hook(t, m, env, "KILL_COUNT", `{"boss":"Vorkath","count":115,"gameMessage":"Your Vorkath kill count is: 115."}`)
	// Unranked on the hiscores, so the day's baseline has 0.
	hook(t, m, env, "KILL_COUNT", `{"boss":"Scurrius","count":4}`)
	hook(t, m, env, "LOOT", `{"source":"Phosani's Nightmare","category":"NPC","killCount":400,"items":[
		{"id":24419,"name":"Inquisitor's great helm","quantity":1,"priceEach":92754634},
		{"id":560,"name":"Death rune","quantity":231,"priceEach":192}]}`)
	hook(t, m, env, "LOOT", `{"source":"Vorkath","category":"NPC","killCount":115,"items":[{"name":"Dragon bones","quantity":2,"priceEach":3000}]}`)
	hook(t, m, env, "CLUE", `{"clueType":"Master","numberCompleted":43,"items":[{"name":"Coins","quantity":5000,"priceEach":1}]}`)
	hook(t, m, env, "LOGIN", `{"world":302,"skills":{"experience":{"Mining":13300000,"Attack":6000000}}}`)
	env.Now = func() time.Time { return later.Add(time.Minute) }
	hook(t, m, env, "PET", `{"petName":"Little nightmare","duplicate":false}`)

	// Not this account, a league, or not used.
	hook(t, m, env, "KILL_COUNT", `{"boss":"Vorkath","count":999}`, `,"playerName":"TariffEvader"`)
	hook(t, m, env, "KILL_COUNT", `{"boss":"Vorkath","count":999}`, `,"seasonalWorld":true`)
	hook(t, m, env, "KILL_COUNT", `{"boss":"Vorkath","count":999}`, `,"seasonalWorld":"true"`)
	hook(t, m, env, "DEATH", `{}`)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "add"}), monitor.ErrBadAction)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, f.count("POST /players/GTX 480"), "WOM isn't asked again so soon")
	helm := "https://oldschool.runescape.wiki/w/Inquisitor%27s_great_helm"
	assert.Equal(t, []monitor.Stat{
		{Label: "Pet", Value: "Little nightmare", Tone: monitor.ToneAccent},
		{Label: "Inquisitor's great helm", Value: "92.8m", Tone: monitor.ToneGood, URL: helm},
		{Label: "Vorkath", Value: "+15", Tone: monitor.ToneGood},
		{Label: "Master clues", Value: "+3", Tone: monitor.ToneGood},
		{Label: "Phosani's Nightmare", Value: "+1", Tone: monitor.ToneGood},
		{Label: "Scurrius", Value: "+1", Tone: monitor.ToneGood},
		{Label: "Mining", Value: "+200.0k xp", Tone: monitor.ToneGood},
	}, res.Summary, "Dink's counts beat WOM's; Scurrius from its first kill Dink saw")

	require.Len(t, res.Blocks, 2)
	since := map[string]string{}
	for _, row := range res.Blocks[0].Rows {
		since[row[0].Text] = row[4].Text
	}
	assert.Equal(t, map[string]string{
		"Vorkath": "5", "Phosani's Nightmare": "0", "Scurrius": "", "Master clues": "", "Mining": "",
	}, since, "kills since the last big drop, only where there's been one; the helm at 400 beats 380")
	assert.Equal(t, "Big drops", res.Blocks[1].Title)
	assert.Equal(t, []monitor.Cell{
		{Text: "20:01", Tone: monitor.ToneMuted}, {Text: "Inquisitor's great helm", URL: helm},
		{Text: "92.8m", Tone: monitor.ToneGood}, {Text: "Phosani's Nightmare, kill 400", Tone: monitor.ToneMuted},
	}, res.Blocks[1].Rows[1])

	require.Len(t, res.Events, 2)
	assert.Equal(t, "Pet: Little nightmare", res.Events[0].Title)
	assert.Equal(t, monitor.PriorityHigh, res.Events[0].Priority)
	assert.True(t, later.Equal(res.Events[1].Time))
	assert.Equal(t, monitor.Event{
		Key: fmt.Sprintf("drop:%d", later.UnixMilli()), Time: res.Events[1].Time, Title: "Inquisitor's great helm · 92.8m",
		Body: "Phosani's Nightmare, kill 400", URL: helm, Priority: monitor.PriorityNormal,
	}, res.Events[1])

	// Tomorrow they're gone; the counts stay.
	env.Now = func() time.Time { return now.Add(5 * time.Hour) }
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks, 1)
	assert.Empty(t, res.Events)
}

func TestMetricFor(t *testing.T) {
	for name, want := range map[string]string{
		"Vorkath":                          "vorkath",
		"Phosani's Nightmare":              "phosanis_nightmare",
		"The Nightmare":                    "nightmare",
		"Nightmare":                        "nightmare",
		"K'ril Tsutsaroth":                 "kril_tsutsaroth",
		"Kree'arra":                        "kreearra",
		"Vet'ion":                          "vetion",
		"TzKal-Zuk":                        "tzkal_zuk",
		"Chambers of Xeric":                "chambers_of_xeric",
		"Chambers of Xeric Challenge Mode": "chambers_of_xeric_challenge_mode",
		"Tombs of Amascut: Expert Mode":    "tombs_of_amascut_expert",
		"Theatre of Blood: Hard Mode":      "theatre_of_blood_hard_mode",
		"Gauntlet":                         "the_gauntlet",
		"Corrupted Gauntlet":               "the_corrupted_gauntlet",
		"Royal Titans":                     "the_royal_titans",
		"Hueycoatl":                        "the_hueycoatl",
		"Wintertodt":                       "wintertodt",
		"Supply crate (Wintertodt)":        "wintertodt",
		"Guardians of the Rift":            "guardians_of_the_rift",
	} {
		got, ok := metricFor(name)
		assert.True(t, ok, name)
		assert.Equal(t, want, got, name)
	}
	for _, none := range []string{"Barrows", "Penance Queen", "Clue Scroll (Master)", "Mining", "Giant rat", ""} {
		_, ok := metricFor(none)
		assert.False(t, ok, none)
	}
	assert.Equal(t, "runecrafting", skillMetric("Runecraft"))
	assert.True(t, samePlayer("gtx_480", "GTX 480"))
}

func TestGP(t *testing.T) {
	assert.Equal(t, "92.8m", gp(92_754_634))
	assert.Equal(t, "50m", gp(50_000_000))
	assert.Equal(t, "1.25b", gp(1_250_000_000))
	assert.Equal(t, "12k", gp(12_345))
	assert.Equal(t, "999", gp(999))
}

func TestDinkPet(t *testing.T) {
	f := newFakeWOM(t)
	m := newMonitor(t, f)
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	hook(t, m, env, "KILL_COUNT", `{"boss":"Vorkath","count":113}`)
	hook(t, m, env, "PET", `{"petName":"Vorki","duplicate":true}`)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "Pet: Vorki", res.Events[0].Title)
	assert.Equal(t, "Vorkath, kill 113. A duplicate: you already have it.", res.Events[0].Body)
	assert.Equal(t, "0", res.Blocks[0].Rows[0][4].Text, "a pet counts as a big drop")
	assert.Equal(t, "Vorkath, kill 113", res.Blocks[1].Rows[0][3].Text)

	// A pet with no kill just before it (or from another boss) is kept,
	// but not put down to anything.
	env.Now = func() time.Time { return now.Add(time.Hour) }
	hook(t, m, env, "PET", `{"petName":"Heron"}`)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "Pet: Heron", res.Events[0].Title)
	assert.Equal(t, "You have a funny feeling like you're being followed.", res.Events[0].Body)
}
