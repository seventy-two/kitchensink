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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func poll(t *testing.T, srv *httptest.Server, player string) monitor.Result {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"url": srv.URL + "/", "player": player})
	m, err := New(raw)
	require.NoError(t, err)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	return res
}

func TestStillToPlay(t *testing.T) {
	res := poll(t, fakeTracker(t), "M")
	require.Len(t, res.Blocks, 1)
	assert.Equal(t, []monitor.Item{
		{Title: "Wordle", URL: "https://wordle.test/"},
		{Title: "Connections", URL: "https://connections.test/"},
	}, res.Blocks[0].Items, "only M's, in the tracker's order")
	assert.Equal(t, []monitor.Stat{{Label: "Dailies left", Value: "2 of 3", Detail: "Next: Wordle"}}, res.Summary)
	assert.Equal(t, 13*time.Hour+40*time.Minute+30*time.Second, res.Next, "just after UK midnight")
}

func TestOtherPlayerAndEndingEarly(t *testing.T) {
	srv := fakeTracker(t)
	res := poll(t, srv, "J")
	assert.Equal(t, "Wikidle", res.Blocks[0].Items[0].Title)

	res = poll(t, fakeTracker(t, "M"), "M")
	assert.Equal(t, "You ended today early.", res.Blocks[0].Text)
	assert.Equal(t, "Ended early", res.Summary[0].Value)
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{"url":"http://x"}`))
	assert.Error(t, err)
}
