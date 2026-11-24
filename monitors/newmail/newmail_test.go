package newmail

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

func post(t *testing.T, m *Monitor, env monitor.Env, mail ...Mail) error {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"mail": mail})
	return m.Act(context.Background(), env, monitor.Action{Type: monitor.HookAction, Data: data})
}

func TestNewMailPanel(t *testing.T) {
	m, err := New(json.RawMessage(`{"hookSecret":"0123456789abcdef0123"}`))
	require.NoError(t, err)
	now := time.Date(2026, 11, 24, 14, 0, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, res.Background, "nothing yet: off the dashboard")

	a := Mail{Key: "me@x:1", Account: "me@x", From: "Priya", Subject: "Sunday  roast?", Date: now.Add(-time.Hour),
		URL: "https://mail.google.com/mail/u/me@x/?tb=1#inbox"}
	b := Mail{Key: "me@x:2", Account: "me@x", From: "Shop", Subject: "Sale", Date: now.Add(-26 * time.Hour), URL: "javascript:alert(1)"}
	require.NoError(t, post(t, m, env, b, a))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.False(t, res.Background)
	require.Len(t, res.Blocks, 1)
	rows := res.Blocks[0].Rows
	require.Len(t, rows, 2)
	assert.Equal(t, "Sunday roast?", rows[0][1].Text, "newest first, spaces tidied")
	assert.Equal(t, "trashbox", rows[0][1].Open)
	assert.Equal(t, "13:00", rows[0][2].Text, "UK time (GMT in November)")
	assert.Equal(t, "Mon 12:00", rows[1][2].Text)
	assert.Empty(t, rows[1][1].URL, "only https links")
	require.Len(t, res.Summary, 1)
	assert.Equal(t, "Priya", res.Summary[0].Label, "closed: the newest, not a count")
	assert.Equal(t, "Sunday roast?", res.Summary[0].Value)
	assert.Empty(t, res.SummaryStyle, "tiles, like To do")
	assert.Equal(t, "13:00", res.Summary[0].Tag)
	assert.Nil(t, res.SummaryBlock)
	assert.Equal(t, "me@x", res.Blocks[0].Rows[0][0].Detail, "the open panel keeps it")
	require.Len(t, res.Events, 2)
	assert.True(t, res.Events[0].Quiet)
	assert.Equal(t, "Mail from Shop", res.Events[0].Title, "oldest first")

	// The same list again (trashbox's heartbeat) is quiet; nothing re-raised.
	assert.ErrorIs(t, post(t, m, env, a, b), monitor.ErrQuiet)
	res, _ = m.Poll(context.Background(), env)
	assert.Empty(t, res.Events)

	// One read elsewhere: gone from the panel.
	require.NoError(t, post(t, m, env, a))
	res, _ = m.Poll(context.Background(), env)
	assert.Len(t, res.Blocks[0].Rows, 1)
	assert.Empty(t, res.Subtitle)

	// trashbox goes quiet: the panel says how old the list is.
	now = now.Add(45 * time.Minute)
	res, _ = m.Poll(context.Background(), env)
	assert.Contains(t, res.Subtitle, "As of 14:00")
}

func TestRefusesOtherActions(t *testing.T) {
	m, _ := New(json.RawMessage(`{"hookSecret":"0123456789abcdef0123"}`))
	env := monitortest.Env(time.Now())
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "add"}), monitor.ErrBadAction)
	_, err := New(json.RawMessage(`{"hookSecret":"short"}`))
	assert.Error(t, err)
}
