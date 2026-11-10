package battery

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

func send(t *testing.T, m *Monitor, env monitor.Env, rs ...Reading) {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"devices": rs})
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "battery", Data: data}))
}

func titles(t *testing.T, m *Monitor, env monitor.Env) []string {
	t.Helper()
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, res.Background)
	var out []string
	for _, e := range res.Events {
		out = append(out, e.Title)
	}
	return out
}

func TestFullOncePerCharge(t *testing.T) {
	m, err := New(nil)
	require.NoError(t, err)
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }

	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 40, Charging: false})
	assert.Empty(t, titles(t, m, env))
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 41, Charging: true})
	assert.Empty(t, titles(t, m, env))
	now = now.Add(time.Hour)
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 100, Charging: true})
	assert.Equal(t, []string{"Basilisk V3 Pro is fully charged"}, titles(t, m, env))
	res, _ := m.Poll(context.Background(), env)
	first := res.Events[0].Key

	// Still on the cable: no second event.
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 100, Charging: true})
	assert.Len(t, titles(t, m, env), 1)

	// Unplugged, then a new charge: a new event with a new key.
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 100, Charging: false})
	now = now.Add(24 * time.Hour)
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 70, Charging: true})
	now = now.Add(time.Hour)
	send(t, m, env, Reading{Name: "Basilisk V3 Pro", Battery: 100, Charging: true})
	res, _ = m.Poll(context.Background(), env)
	require.Len(t, res.Events, 2)
	assert.NotEqual(t, first, res.Events[1].Key)
}

func TestMergeCableAndDongle(t *testing.T) {
	got := Merge([]Reading{
		{Name: "Basilisk V3 Pro", Battery: 99, Charging: false},
		{Name: "Basilisk V3 Pro", Battery: 100, Charging: true},
		{Name: "DeathAdder V3 Pro", Battery: 55},
		{Name: "Keyboard", Battery: -1},
		{Name: "", Battery: 50},
	})
	assert.Equal(t, []Reading{{Name: "Basilisk V3 Pro", Battery: 100, Charging: true}, {Name: "DeathAdder V3 Pro", Battery: 55}}, got)
}

func TestBadActions(t *testing.T) {
	m, _ := New(nil)
	env := monitortest.Env(time.Now())
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "nope"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "battery", Data: json.RawMessage(`{`)}), monitor.ErrBadAction)
	_, err := New(json.RawMessage(`{"full":0}`))
	assert.Error(t, err)
}
