package todo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)

// list is the checklist's items as "[x] text" lines, and the result.
func list(t *testing.T, m *Monitor, env monitor.Env) ([]string, monitor.Result) {
	t.Helper()
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks, 1)
	b := res.Blocks[0]
	assert.Equal(t, monitor.BlockChecklist, b.Type)
	assert.NotEmpty(t, b.AddHint)
	var out []string
	for _, it := range b.Items {
		mark := "[ ] "
		if it.Done {
			mark = "[x] "
		}
		out = append(out, mark+it.Title)
	}
	return out, res
}

func idOf(t *testing.T, m *Monitor, env monitor.Env, text string) string {
	t.Helper()
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	for _, it := range res.Blocks[0].Items {
		if it.Title == text {
			return it.ID
		}
	}
	t.Fatalf("no task %q", text)
	return ""
}

func TestList(t *testing.T) {
	m, err := New(nil)
	require.NoError(t, err)
	env := monitortest.Env(now)
	ctx := context.Background()

	got, res := list(t, m, env)
	assert.Empty(t, got)
	assert.Equal(t, []monitor.Stat{{Label: "To do", Value: "All done", Tone: monitor.ToneGood}}, res.Summary)

	for _, text := range []string{"Bins out", "  Book   dentist ", "Renew passport", "Call Mum"} {
		require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "add", Text: text}))
	}
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "toggle", ID: idOf(t, m, env, "Bins out")}))
	got, res = list(t, m, env)
	assert.Equal(t, []string{"[ ] Book dentist", "[ ] Renew passport", "[ ] Call Mum", "[x] Bins out"}, got,
		"open first, oldest first, then ticked; spacing tidied")
	assert.Equal(t, []monitor.Stat{
		{Label: "To do", Value: "Book dentist"},
		{Value: "Renew passport"},
		{Value: "Call Mum"},
	}, res.Summary)
	assert.Equal(t, 24*time.Hour+time.Second, res.Next, "polls again when the ticked one clears")

	// Unticking puts it back; deleting removes it.
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "toggle", ID: idOf(t, m, env, "Bins out")}))
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "delete", ID: idOf(t, m, env, "Call Mum")}))
	got, res = list(t, m, env)
	assert.Equal(t, []string{"[ ] Bins out", "[ ] Book dentist", "[ ] Renew passport"}, got)
	assert.Zero(t, res.Next, "nothing to clear")

	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "add", Text: "Fourth"}))
	_, res = list(t, m, env)
	assert.Equal(t, monitor.Stat{Label: "And", Value: "1 more", Tone: monitor.ToneMuted}, res.Summary[3])
}

func TestClearsAfterADay(t *testing.T) {
	m, err := New(nil)
	require.NoError(t, err)
	env := monitortest.Env(now)
	ctx := context.Background()
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "add", Text: "Early"}))
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "add", Text: "Late"}))
	// Ticked in the other order from how they were added.
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: "toggle", ID: idOf(t, m, env, "Late")}))
	later := env
	later.Now = func() time.Time { return now.Add(6 * time.Hour) }
	require.NoError(t, m.Act(ctx, later, monitor.Action{Type: "toggle", ID: idOf(t, m, later, "Early")}))

	at := func(d time.Duration) monitor.Env {
		e := env
		e.Now = func() time.Time { return now.Add(d) }
		return e
	}
	got, res := list(t, m, at(23*time.Hour))
	assert.Equal(t, []string{"[x] Early", "[x] Late"}, got, "both still shown, latest ticked first")
	assert.Equal(t, time.Hour+time.Second, res.Next)

	got, res = list(t, m, at(24*time.Hour))
	assert.Equal(t, []string{"[x] Early"}, got, "a day after it was ticked, it's gone")
	assert.Equal(t, 6*time.Hour+time.Second, res.Next)

	got, _ = list(t, m, at(30*time.Hour))
	assert.Empty(t, got)

	short, err := New(json.RawMessage(`{"clearAfter": "1h"}`))
	require.NoError(t, err)
	assert.Equal(t, time.Hour, short.clearAfter)
}

func TestBadActions(t *testing.T) {
	m, err := New(nil)
	require.NoError(t, err)
	env := monitortest.Env(now)
	ctx := context.Background()
	for _, a := range []monitor.Action{
		{Type: "add", Text: "   "},
		{Type: "add", Text: strings.Repeat("x", maxText+1)},
		{Type: "toggle", ID: "missing"},
		{Type: "delete", ID: "missing"},
		{Type: "rename"},
	} {
		assert.ErrorIs(t, m.Act(ctx, env, a), monitor.ErrBadAction, a)
	}
	_, err = New(json.RawMessage(`{"clearAfter": "-1h"}`))
	assert.Error(t, err)
}
