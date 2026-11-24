package codes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
)

func hook(t *testing.T, m *Monitor, env monitor.Env, p map[string]string) error {
	t.Helper()
	data, _ := json.Marshal(p)
	return m.Act(context.Background(), env, monitor.Action{Type: monitor.HookAction, Data: data})
}

func TestCodeShownThenWiped(t *testing.T) {
	m, err := New(json.RawMessage(`{"hookSecret":"0123456789abcdef0123"}`))
	require.NoError(t, err)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }

	require.NoError(t, hook(t, m, env, map[string]string{"from": "Fernhill Bank", "code": "482913", "account": "me@example.com"}))
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, res.Background)
	require.Len(t, res.Events, 1)
	e := res.Events[0]
	assert.Equal(t, "Code from Fernhill Bank", e.Title)
	assert.Equal(t, "482913", e.Body)
	assert.Equal(t, monitor.PriorityHigh, e.Priority)
	assert.Contains(t, e.Key, "code:")
	assert.Equal(t, now.Add(10*time.Minute), e.Expires)
	assert.Equal(t, 10*time.Minute+time.Second, res.Next, "polls again to wipe it")

	// A retry of the same code is quiet; a poll before expiry says nothing new.
	assert.ErrorIs(t, hook(t, m, env, map[string]string{"from": "Fernhill Bank", "code": "482913"}), monitor.ErrQuiet)
	now = now.Add(5 * time.Minute)
	res, _ = m.Poll(context.Background(), env)
	assert.Empty(t, res.Events)

	// Once its time's up, the event is rewritten without the code.
	now = now.Add(5*time.Minute + time.Second)
	res, _ = m.Poll(context.Background(), env)
	require.Len(t, res.Events, 1)
	w := res.Events[0]
	assert.Equal(t, e.Key, w.Key)
	assert.True(t, w.Update)
	assert.Empty(t, w.Body)
	assert.Equal(t, "Code from Fernhill Bank (expired)", w.Title)
	res, _ = m.Poll(context.Background(), env)
	assert.Empty(t, res.Events, "forgotten after wiping")
	assert.Zero(t, res.Next)
}

func TestWipeSurvivesRestart(t *testing.T) {
	cfg := json.RawMessage(`{"hookSecret":"0123456789abcdef0123"}`)
	m, _ := New(cfg)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }
	require.NoError(t, hook(t, m, env, map[string]string{"from": "Shop", "code": "052736"}))
	res, _ := m.Poll(context.Background(), env)
	require.Len(t, res.Events, 1)
	key := res.Events[0].Key

	// The saved state never holds the code.
	var saved []map[string]any
	_, err := env.State.Get(wipesKey, &saved)
	require.NoError(t, err)
	raw, _ := json.Marshal(saved)
	assert.NotContains(t, string(raw), "052736")

	// kitchensink restarts: a new monitor, same state.
	m2, _ := New(cfg)
	now = now.Add(11 * time.Minute)
	res, _ = m2.Poll(context.Background(), env)
	require.Len(t, res.Events, 1)
	assert.Equal(t, key, res.Events[0].Key)
	assert.True(t, res.Events[0].Update)
	assert.Empty(t, res.Events[0].Body)
}

func TestRefusesNonCodes(t *testing.T) {
	m, err := New(json.RawMessage(`{"hookSecret":"0123456789abcdef0123"}`))
	require.NoError(t, err)
	env := monitortest.Env(time.Now())
	for _, c := range []string{"", "12", "<script>", "123456789012345", "12 34"} {
		assert.ErrorIs(t, hook(t, m, env, map[string]string{"from": "x", "code": c}), monitor.ErrBadAction, c)
	}
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "other"}), monitor.ErrBadAction)
}

func TestNeedsALongSecret(t *testing.T) {
	_, err := New(json.RawMessage(`{"hookSecret":"short"}`))
	assert.Error(t, err)
	_, err = New(nil)
	assert.Error(t, err)
}
