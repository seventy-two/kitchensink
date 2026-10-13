package calendar

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func upsert(t *testing.T, m *Monitor, env monitor.Env, e Added) error {
	t.Helper()
	data, err := json.Marshal(e)
	require.NoError(t, err)
	return m.Act(context.Background(), env, monitor.Action{Type: "upsert", Data: data})
}

func TestAdded(t *testing.T) {
	_, personal := feeds(t)
	m := newMonitor(t, personal)
	env := monitortest.Env(now)
	ctx := context.Background()

	gig := Added{Key: "mail:ticketmaster:123", Title: "Vildhjarta", Source: "Email", Location: "The Fleece, Bristol",
		Start: time.Date(2026, 10, 13, 19, 0, 0, 0, london), Detail: "2 tickets · Ticketmaster"}
	require.NoError(t, upsert(t, m, env, gig))
	require.NoError(t, upsert(t, m, env, Added{Key: "mail:waitrose:9", Title: "Waitrose delivery", Source: "Email",
		Start: time.Date(2026, 10, 12, 10, 0, 0, 0, london), End: time.Date(2026, 10, 12, 11, 0, 0, 0, london)}))
	require.NoError(t, upsert(t, m, env, Added{Key: "mail:amazon:1", Title: "Amazon parcel", Source: "Email", AllDay: true,
		Start: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}))
	require.NoError(t, upsert(t, m, env, Added{Key: "old", Title: "Long gone", Start: now.AddDate(0, 0, -3)}))

	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"All day | Bins out | Personal",
		"10:00–11:00 | Waitrose delivery | Email",
	}, rows(res.Blocks[0]), "the calendar column shows once something's added")
	assert.Equal(t, monitor.ToneWarn, res.Blocks[0].Rows[1][2].Tone)
	tue := res.Blocks[1]
	assert.Equal(t, "19:00 | Vildhjarta · The Fleece, Bristol | Email", rows(tue)[0])
	assert.Equal(t, "2 tickets · Ticketmaster", tue.Rows[0][1].Detail, "its own detail stands in for a note")
	assert.Contains(t, rows(res.Blocks[2]), "All day | Amazon parcel | Email")
	assert.Equal(t, monitor.Stat{Label: "10:00", Value: "Waitrose delivery", Detail: "Email"}, res.Summary[0])

	// Reminders work as for any timed event.
	res, err = m.Poll(ctx, at(env, time.Date(2026, 10, 12, 9, 52, 0, 0, london)))
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "Waitrose delivery at 10:00", res.Events[0].Title)

	// Moved, then cancelled.
	gig.Start = gig.Start.Add(30 * time.Minute)
	require.NoError(t, upsert(t, m, env, gig))
	res, _ = m.Poll(ctx, env)
	assert.Equal(t, "19:30 | Vildhjarta · The Fleece, Bristol | Email", rows(res.Blocks[1])[0])
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "remove", ID: gig.Key}))
	res, _ = m.Poll(ctx, env)
	assert.Equal(t, []string{"22:00–… | Late film | Personal"}, rows(res.Blocks[1]))

	added, err := loadAdded(env)
	require.NoError(t, err)
	assert.NotContains(t, added, "old", "forgotten once well past")

	assert.ErrorIs(t, upsert(t, m, env, Added{Key: "x", Title: " "}), monitor.ErrBadAction)
	assert.ErrorIs(t, upsert(t, m, env, Added{Key: "x", Title: "x", Start: now, End: now.Add(-time.Hour)}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(ctx, env, monitor.Action{Type: "remove", ID: "nope"}), monitor.ErrBadAction)
}

func at(env monitor.Env, t time.Time) monitor.Env {
	env.Now = func() time.Time { return t }
	return env
}
