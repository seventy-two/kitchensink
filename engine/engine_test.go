package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scripted returns each result in turn.
type scripted struct {
	mu      sync.Mutex
	results []monitor.Result
	errs    []error
	polls   int
}

func (s *scripted) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := min(s.polls, len(s.results)-1)
	s.polls++
	return s.results[i], s.errs[i]
}

type recorder struct {
	mu     sync.Mutex
	events []monitor.Event
}

func (r *recorder) Notify(ctx context.Context, e monitor.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func newEngine(t *testing.T, m monitor.Monitor, n Notifier) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	e, err := New(st, []Spec{{ID: "m", Kind: "fake", Title: "Fake", Interval: time.Hour, Monitor: m}},
		Options{Notifiers: []Notifier{n}, Now: func() time.Time { return now }})
	require.NoError(t, err)
	return e, st
}

func block(text string) []monitor.Block {
	return []monitor.Block{{Type: monitor.BlockText, Text: text}}
}

func TestPollRecordsAndNotifies(t *testing.T) {
	m := &scripted{
		results: []monitor.Result{
			{Blocks: block("one"), Events: []monitor.Event{{Key: "a", Title: "A"}, {Key: "q", Title: "Q", Quiet: true}}},
			{Blocks: block("two"), Events: []monitor.Event{{Key: "a", Title: "A again"}, {Key: "b", Title: "B"}}, Next: time.Minute},
			{},
		},
		errs: []error{nil, nil, errors.New("site down")},
	}
	n := &recorder{}
	e, st := newEngine(t, m, n)
	msgs, stop := e.Subscribe()
	defer stop()

	r := e.runners[0]
	assert.Equal(t, time.Hour, e.poll(context.Background(), r))
	assert.Equal(t, time.Minute, e.poll(context.Background(), r), "Next brings the poll forward")

	// Repeated keys are dropped; quiet events are stored but not notified.
	events, err := st.EventsSince(0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"A", "Q", "B"}, titles(events))
	assert.Equal(t, monitor.PriorityNormal, events[0].Priority)
	assert.Equal(t, "m", events[0].Monitor)
	assert.True(t, events[0].Time.Equal(now))
	assert.Equal(t, []string{"A", "B"}, titles(n.events))

	// A failed poll keeps the last blocks and reports the error.
	assert.Equal(t, time.Hour, e.poll(context.Background(), r))
	snaps := e.Snapshots()
	require.Len(t, snaps, 1)
	assert.Equal(t, "site down", snaps[0].Error)
	assert.Equal(t, "two", snaps[0].Blocks[0].Text)
	assert.Equal(t, "Fake", snaps[0].Title)

	// Subscribers saw snapshots and new events in order.
	var got []string
	for len(msgs) > 0 {
		m := <-msgs
		if m.Type == MessageEvent {
			got = append(got, "event "+m.Event.Title)
		} else {
			got = append(got, "snapshot")
		}
	}
	assert.Equal(t, []string{"snapshot", "event A", "event Q", "snapshot", "event B", "snapshot"}, got)
}

func TestSnapshotsSurviveRestart(t *testing.T) {
	m := &scripted{results: []monitor.Result{{Blocks: block("saved"), Subtitle: "watching", SummaryStyle: monitor.SummaryChips, Summary: []monitor.Stat{{Label: "Next", Value: "soon"}}}}, errs: []error{nil}}
	e, st := newEngine(t, m, &recorder{})
	e.poll(context.Background(), e.runners[0])

	e2, err := New(st, []Spec{{ID: "m", Kind: "fake", Title: "Renamed", Interval: time.Hour, Monitor: m}}, Options{})
	require.NoError(t, err)
	snap := e2.Snapshots()[0]
	assert.Equal(t, "saved", snap.Blocks[0].Text)
	assert.Equal(t, "soon", snap.Summary[0].Value)
	assert.Equal(t, "watching", snap.Subtitle)
	assert.Equal(t, monitor.SummaryChips, snap.SummaryStyle)
	assert.Equal(t, "Renamed", snap.Title, "titles come from the config")
}

func TestRunAndRefresh(t *testing.T) {
	m := &scripted{results: []monitor.Result{{}}, errs: []error{nil}}
	e, _ := newEngine(t, m, &recorder{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()

	polls := func() int {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.polls
	}
	require.Eventually(t, func() bool { return polls() == 1 }, time.Second, time.Millisecond)
	assert.True(t, e.Refresh("m"))
	assert.False(t, e.Refresh("nope"))
	require.Eventually(t, func() bool { return polls() == 2 }, time.Second, time.Millisecond, "refresh polls straight away")

	cancel()
	<-done
}

func TestDuplicateIDs(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	defer st.Close()
	_, err = New(st, []Spec{{ID: "a"}, {ID: "a"}}, Options{})
	assert.Error(t, err)
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	var h hub
	h.subs = map[chan Message]struct{}{}
	ch, stop := h.subscribe()
	for range cap(ch) + 1 {
		h.publish(Message{})
	}
	n := 0
	for range ch {
		n++
	}
	assert.Equal(t, cap(ch), n, "the channel is closed once full")
	stop() // safe after the hub has closed it
}

func titles(events []monitor.Event) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Title)
	}
	return out
}

// acting is a monitor that takes actions, recording them in its state.
type acting struct{ scripted }

func (a *acting) Act(ctx context.Context, env monitor.Env, act monitor.Action) error {
	if act.Type != "add" {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, act.Type)
	}
	return env.State.Set("last", act.Text)
}

func TestAct(t *testing.T) {
	m := &acting{scripted{results: []monitor.Result{{}}, errs: []error{nil}}}
	e, st := newEngine(t, m, &recorder{})
	ctx := context.Background()

	require.NoError(t, e.Act(ctx, "m", monitor.Action{Type: "add", Text: "Bins"}))
	var last string
	ok, err := st.State("m").Get("last", &last)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Bins", last, "the monitor got its state")
	select {
	case <-e.runners[0].refresh:
	default:
		t.Fatal("no poll was asked for after the action")
	}

	assert.ErrorIs(t, e.Act(ctx, "m", monitor.Action{Type: "fly"}), monitor.ErrBadAction)
	assert.ErrorIs(t, e.Act(ctx, "nope", monitor.Action{Type: "add"}), ErrNoMonitor)

	plain, _ := newEngine(t, &scripted{results: []monitor.Result{{}}, errs: []error{nil}}, &recorder{})
	assert.ErrorIs(t, plain.Act(ctx, "m", monitor.Action{Type: "add"}), ErrNoActions)
}
