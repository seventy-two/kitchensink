package screens

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

// Wednesday 18 Nov 2026, 14:00 in London (GMT).
var now = time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)

// fake serves kino's cinema and boxset's state from the given JSON,
// checking each gets its header.
func fake(t *testing.T, cinema, state *string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/cinema", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Kino-Key") != "key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(*cinema))
	})
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Player") != "M" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(*state))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newMonitor(t *testing.T, srv *httptest.Server) *Monitor {
	raw, _ := json.Marshal(Config{KinoURL: srv.URL, KinoKey: "key", BoxsetURL: srv.URL, BoxsetLink: "https://example.com/boxset/"})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func texts(b monitor.Block) [][]string {
	var out [][]string
	for _, r := range b.Rows {
		var line []string
		for _, c := range r {
			line = append(line, c.Text)
		}
		out = append(out, line)
	}
	return out
}

func TestNothingOn(t *testing.T) {
	cinema := `{"on": [], "past": [{"film": {"label": "Heat (1995)"}, "status": "done"}]}`
	state := `{"state": {"items": [{"show": {"name": "Scrubs"}, "code": "S01E01", "status": "done", "watched": true}]}}`
	res, err := newMonitor(t, fake(t, &cinema, &state)).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.True(t, res.Background, "nothing on keeps it off the dashboard")
	assert.Nil(t, res.SummaryBlock)
	assert.True(t, res.Relevant.Until.IsZero())
}

func TestBookedAndQueued(t *testing.T) {
	cinema := `{"on": [
		{"url": "https://k/cinema/2", "film": {"label": "Alien (1979)"}, "bookedBy": {"name": "Megz"}, "starts": "2026-11-21T20:00:00Z", "status": "scheduled", "statusText": "Booked"},
		{"url": "https://k/cinema/1", "film": {"label": "Heat (1995)"}, "bookedBy": {"username": "jo"}, "starts": "2026-11-18T13:00:00Z", "status": "downloading", "statusText": "Downloading 40%", "late": true}
	]}`
	state := `{"state": {"items": [
		{"show": {"name": "Scrubs"}, "code": "S01E01", "status": "ended", "watched": true},
		{"show": {"name": "Scrubs"}, "code": "S01E02", "status": "ready", "statusText": "Ready"},
		{"show": {"name": "Scrubs"}, "code": "S01E03", "status": "downloading", "statusText": "Downloading 10%"}
	]}}`
	res, err := newMonitor(t, fake(t, &cinema, &state)).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.False(t, res.Background)
	assert.Equal(t, [][]string{
		{"Cinema", "Alien (1979) · Megz", "Sat 20:00"},
		{"Cinema", "Heat (1995) · jo", "Today 13:00 · Downloading 40%"},
		{"Boxset", "Scrubs S01E02", "Ready · 1 more queued"},
	}, texts(res.Blocks[0]))
	assert.Equal(t, monitor.ToneWarn, res.Blocks[0].Rows[1][2].Tone, "late")
	assert.Equal(t, "https://k/cinema/2", res.Blocks[0].Rows[0][1].URL)
	assert.Equal(t, "https://example.com/boxset/", res.Blocks[0].Rows[2][1].URL)
	require.NotNil(t, res.SummaryBlock)
	assert.Len(t, res.Summary, 3)
	assert.True(t, res.Relevant.Until.IsZero(), "nothing's on yet")
}

func TestLiveFirst(t *testing.T) {
	cinema := `{"on": [
		{"film": {"label": "Alien (1979)"}, "starts": "2026-11-19T20:00:00Z", "status": "scheduled"},
		{"film": {"label": "Heat (1995)"}, "starts": "2026-11-18T13:00:00Z", "status": "playing", "paused": true}
	]}`
	state := `{"state": {"items": [{"show": {"name": "Scrubs"}, "code": "S01E02", "status": "playing"}]}}`
	res, err := newMonitor(t, fake(t, &cinema, &state)).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"Cinema", "Heat (1995)", "Paused"},
		{"Boxset", "Scrubs S01E02", "On now"},
		{"Cinema", "Alien (1979)", "Tomorrow 20:00"},
	}, texts(res.Blocks[0]))
	assert.Equal(t, monitor.ToneGood, res.Blocks[0].Rows[1][2].Tone)
	assert.True(t, res.Relevant.Contains(now))
}

func TestKinoRefuses(t *testing.T) {
	cinema, state := `{}`, `{}`
	srv := fake(t, &cinema, &state)
	raw, _ := json.Marshal(Config{KinoURL: srv.URL, KinoKey: "wrong"})
	m, err := New(raw)
	require.NoError(t, err)
	_, err = m.Poll(context.Background(), monitortest.Env(now))
	assert.ErrorContains(t, err, "kino: 401")
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{}`))
	assert.Error(t, err)
	_, err = New(json.RawMessage(`{"kinoUrl": "http://x"}`))
	assert.Error(t, err, "kino needs its key")
}
