package osrs

import (
	"context"
	"encoding/json"
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
	raw, _ := json.Marshal(map[string]any{"player": "GTX 480", "womURL": f.URL})
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
	assert.Equal(t, "GTX 480 · since midnight", res.Subtitle)
	assert.Equal(t, []monitor.Stat{
		{Label: "Vorkath", Value: "+12", Tone: monitor.ToneGood},
		{Label: "Master clues", Value: "+2", Tone: monitor.ToneGood},
		{Label: "Scurrius", Value: "+1", Tone: monitor.ToneGood},
		{Label: "Mining", Value: "+150.0k xp", Tone: monitor.ToneGood},
	}, res.Summary, "kills first, then XP; no attack XP or Barrows")
	require.Len(t, res.Blocks, 1)
	assert.Equal(t, []monitor.Cell{
		{Text: "Vorkath"}, {Text: "+12", Tone: monitor.ToneGood}, {Text: "112", Tone: monitor.ToneMuted}, {Text: "Vorki", Tone: monitor.ToneMuted},
	}, res.Blocks[0].Rows[0])
	assert.Equal(t, "13.25M xp", res.Blocks[0].Rows[3][2].Text)
	assert.Equal(t, 4*time.Hour+time.Minute, res.Next, "wakes just after midnight to mark the new day")

	// Later the same day: WOM refuses an update so soon, so the latest
	// snapshot does; the day's start is kept.
	f.mu.Lock()
	f.limited = true
	f.mu.Unlock()
	env.Now = func() time.Time { return now.Add(30 * time.Second) }
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
	assert.Equal(t, []monitor.Stat{{Label: "Today", Value: "Nothing yet", Tone: monitor.ToneMuted}}, summary(nil))
	assert.Equal(t, "Nothing towards a pet yet today.", table(nil).Text)
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
