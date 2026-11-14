package claude

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
)

// sample is the "limits" list as Claude sent it on 14 Nov 2026.
const sample = `{"limits":[
 {"kind":"session","group":"session","percent":77,"severity":"warning","resets_at":"2026-11-14T04:50:00.817949+00:00","scope":null,"is_active":true},
 {"kind":"weekly_all","group":"weekly","percent":62,"severity":"normal","resets_at":"2026-11-15T03:00:00.817965+00:00","scope":null,"is_active":false},
 {"kind":"weekly_scoped","group":"weekly","percent":56,"severity":"normal","resets_at":"2026-11-15T03:00:00.818081+00:00","scope":{"model":{"id":null,"display_name":"Fable"},"surface":null},"is_active":false}]}`

func TestUsage(t *testing.T) {
	m, err := New(nil)
	require.NoError(t, err)
	now := time.Date(2026, 11, 14, 3, 16, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, monitor.BlockText, res.Blocks[0].Type, "no reading yet")

	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "usage", Data: []byte(sample)}))
	now = now.Add(5 * time.Minute)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []monitor.Stat{
		{Label: "Session", Value: "77%", Detail: "resets 04:50", Tone: monitor.ToneWarn},
		{Label: "Week", Value: "62%", Detail: "resets Sun 03:00"},
		{Label: "Fable", Value: "56%", Detail: "resets Sun 03:00"},
	}, res.Summary)
	assert.Equal(t, "Read 5m ago", res.Subtitle)
	assert.True(t, res.Timely)
	assert.Equal(t, 89*time.Minute+1*time.Second, res.Next.Truncate(time.Second), "repolls when the session resets")

	now = time.Date(2026, 11, 14, 5, 0, 0, 0, time.UTC)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, monitor.Stat{Label: "Session", Value: "0%", Detail: "reset"}, res.Summary[0], "a passed reset shows as empty")
	assert.Contains(t, res.Subtitle, "hasn't run since")
}

func TestBadAction(t *testing.T) {
	m, _ := New(nil)
	env := monitortest.Env(time.Now())
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "usage", Data: []byte(`{"limits":[]}`)}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "nope"}), monitor.ErrBadAction)
}

func TestTone(t *testing.T) {
	now := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	s := Stats([]Limit{{Kind: "session", Percent: 95, ResetsAt: now.Add(time.Hour)}}, now)
	assert.Equal(t, monitor.ToneBad, s[0].Tone)
}
