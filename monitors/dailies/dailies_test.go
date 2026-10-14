package dailies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 10:20 in the UK on 14 October.
var now = time.Date(2026, 10, 14, 9, 20, 0, 0, time.UTC)

// fakeTracker serves three games; J has played two of them and M one,
// and ends lists who's ended the day early.
func fakeTracker(t *testing.T, ends ...string) *httptest.Server {
	js := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"today": "2026-10-14", "games": []any{
			map[string]any{"id": "wordle", "name": "Wordle", "url": "https://wordle.test/"},
			map[string]any{"id": "connections", "name": "Connections", "url": "https://connections.test/"},
			map[string]any{"id": "wikidle", "name": "Wikidle", "url": "https://wikidle.test/"},
		}})
	})
	mux.HandleFunc("GET /api/day", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "2026-10-14", r.URL.Query().Get("date"))
		e := []any{}
		for _, p := range ends {
			e = append(e, map[string]any{"player": p})
		}
		js(w, map[string]any{"ends": e, "results": []any{
			map[string]any{"game": "wordle", "player": "J"},
			map[string]any{"game": "connections", "player": "J"},
			map[string]any{"game": "wikidle", "player": "M"},
		}})
	})
	mux.HandleFunc("POST /api/share", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Player, Text string }
		json.NewDecoder(r.Body).Decode(&in)
		switch in.Text {
		case "Wordle 1,578 4/6":
			js(w, map[string]any{"message": "Logged " + in.Player + "'s Wordle: 4/6."})
		case "broken":
			w.WriteHeader(http.StatusInternalServerError)
			js(w, map[string]any{"error": "saving: disk full"})
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			js(w, map[string]any{"error": "Couldn't tell which game that's from."})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func poll(t *testing.T, srv *httptest.Server, player string) monitor.Result {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"url": srv.URL + "/", "player": player, "names": map[string]string{"wordle": "W"}})
	m, err := New(raw)
	require.NoError(t, err)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	return res
}

func TestToday(t *testing.T) {
	res := poll(t, fakeTracker(t), "M")
	require.Len(t, res.Blocks, 1)
	notYet := monitor.Cell{}
	assert.Equal(t, [][]monitor.Cell{
		{{Text: "Wordle", URL: "https://wordle.test/"}, notYet},
		{{Text: "Connections", URL: "https://connections.test/"}, notYet},
		{{Text: "Wikidle", URL: "https://wikidle.test/"}, {Text: "Done", Tone: monitor.ToneGood}},
	}, res.Blocks[0].Rows, "M's, in the tracker's order")
	assert.Equal(t, []monitor.Stat{
		{Value: "W", URL: "https://wordle.test/"},
		{Value: "Connections", URL: "https://connections.test/"},
		{Value: "Wikidle", URL: "https://wikidle.test/", Tone: monitor.ToneGood},
	}, res.Summary, "short names where given")
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	assert.Equal(t, 13*time.Hour+40*time.Minute+30*time.Second, res.Next, "just after UK midnight")
}

func TestOtherPlayerAndEndingEarly(t *testing.T) {
	res := poll(t, fakeTracker(t), "J")
	assert.Equal(t, []monitor.Tone{monitor.ToneGood, monitor.ToneGood, ""}, tones(res.Summary))

	res = poll(t, fakeTracker(t, "M"), "M")
	assert.Equal(t, []monitor.Tone{monitor.ToneMuted, monitor.ToneMuted, monitor.ToneGood}, tones(res.Summary))
	assert.Equal(t, "Skipped", res.Blocks[0].Rows[0][1].Text)
}

func tones(stats []monitor.Stat) []monitor.Tone {
	var out []monitor.Tone
	for _, s := range stats {
		out = append(out, s.Tone)
	}
	return out
}

func TestPaste(t *testing.T) {
	srv := fakeTracker(t)
	raw, _ := json.Marshal(map[string]any{"url": srv.URL, "player": "M"})
	m, err := New(raw)
	require.NoError(t, err)
	env := monitortest.Env(now)
	ctx := context.Background()

	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "paste", Text: "Wordle 1,578 4/6"}))
	env.Now = func() time.Time { return now.Add(time.Second) }
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "paste", Text: "a shopping list"}))
	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.Equal(t, "Logged M's Wordle: 4/6.", res.Events[0].Title)
	assert.True(t, res.Events[0].Quiet)
	assert.True(t, res.Events[0].Expires.Equal(now.Add(2*time.Minute)), "only worth seeing now")
	assert.Equal(t, "Not logged", res.Events[1].Title)
	assert.Equal(t, "Couldn't tell which game that's from.", res.Events[1].Body)
	assert.True(t, res.Events[1].Quiet)
	assert.True(t, res.Events[1].Expires.Equal(now.Add(2*time.Minute+time.Second)))
	assert.NotEqual(t, res.Events[0].Key, res.Events[1].Key)
	assert.NotEmpty(t, res.Paste)

	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "each told once")

	assert.ErrorContains(t, m.Act(ctx, env, monitor.Action{Type: "paste", Text: "broken"}), "disk full")
	assert.ErrorIs(t, m.Act(ctx, env, monitor.Action{Type: "toggle"}), monitor.ErrBadAction)
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{"url":"http://x"}`))
	assert.Error(t, err)
}

func TestVersus(t *testing.T) {
	m, err := New(json.RawMessage(`{"url": "http://tracker.test", "player": "M", "versus": "J"}`))
	require.NoError(t, err)
	var d day
	require.NoError(t, json.Unmarshal([]byte(`{
		"results": [
			{"game": "wordle", "player": "M"}, {"game": "wordle", "player": "J"},
			{"game": "flagle", "player": "M"}, {"game": "flagle", "player": "J"},
			{"game": "maptap", "player": "M"}, {"game": "maptap", "player": "J"},
			{"game": "krillion", "player": "M"},
			{"game": "fillle", "player": "J"}
		],
		"winners": {"wordle": "M", "flagle": "J", "maptap": ""}
	}`), &d))
	got, ended := m.outcomes(d)
	assert.False(t, ended)
	assert.Equal(t, map[string]outcome{"wordle": won, "flagle": lost, "maptap": drew, "krillion": waiting}, got)
	assert.Equal(t, unplayed, outcomeOf(got, ended, "fillle"))

	games := []game{{ID: "wordle"}, {ID: "flagle"}, {ID: "maptap"}, {ID: "krillion"}, {ID: "fillle"}}
	var tones []monitor.Tone
	for _, st := range m.chips(games, got, ended) {
		tones = append(tones, st.Tone)
	}
	assert.Equal(t, []monitor.Tone{monitor.ToneGood, monitor.ToneBad, monitor.ToneAccent, monitor.ToneAccent, monitor.ToneNone}, tones)
	assert.Equal(t, "Waiting for J", m.status(waiting).Text)
}
