package football

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

var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newMonitor(t *testing.T, scoreboard, sleeper string) *Monitor {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"nfl":     map[string]any{"scoreboardURL": scoreboard, "teams": []string{"CHI"}, "timezone": "Europe/London"},
		"fantasy": map[string]any{"sleeperUser": "someone", "sleeperURL": sleeper, "siteURL": sleeper},
	})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestNeedsBothParts(t *testing.T) {
	_, err := New(json.RawMessage(`{"nfl": {}}`))
	assert.Error(t, err, "fantasy needs a user or league")
}

// With fantasy down, the NFL still shows, its state under its own prefix;
// with both down, the poll fails.
func TestOnePartFailing(t *testing.T) {
	sb := monitortest.Serve(t, "../nfl/testdata/scoreboard.json")
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)

	m := newMonitor(t, sb.URL, down.URL)
	env := monitortest.Env(now)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	require.NotEmpty(t, res.Summary)
	assert.Equal(t, "CHI @ GB", res.Summary[0].Label, "the followed team's game")
	assert.True(t, res.Timely, "a game's on")
	assert.Contains(t, monitortest.Keys(res.Events), "kickoff:401872990")
	var scores any
	ok, err := env.State.Get("nfl/scores", &scores)
	require.NoError(t, err)
	assert.True(t, ok, "the NFL's state is under nfl/")

	m = newMonitor(t, down.URL, down.URL)
	_, err = m.Poll(context.Background(), env)
	assert.Error(t, err)
}
