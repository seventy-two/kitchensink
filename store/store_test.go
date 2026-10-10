package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEvents(t *testing.T) {
	s := open(t)
	at := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	e, isNew, err := s.AddEvent(monitor.Event{Monitor: "a", Key: "1", Time: at, Title: "first"})
	require.NoError(t, err)
	assert.True(t, isNew)
	assert.EqualValues(t, 1, e.ID)

	_, isNew, err = s.AddEvent(monitor.Event{Monitor: "a", Key: "1", Title: "again"})
	require.NoError(t, err)
	assert.False(t, isNew, "same monitor and key")

	_, isNew, err = s.AddEvent(monitor.Event{Monitor: "b", Key: "1", Title: "other monitor"})
	require.NoError(t, err)
	assert.True(t, isNew, "keys are per monitor")
	_, _, err = s.AddEvent(monitor.Event{Monitor: "a", Key: "2", Title: "third"})
	require.NoError(t, err)

	_, _, err = s.AddEvent(monitor.Event{Monitor: "a"})
	assert.Error(t, err, "events need a key")

	all, err := s.EventsSince(0, 10)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "first", all[0].Title)
	assert.True(t, all[0].Time.Equal(at))
	assert.EqualValues(t, 3, all[2].ID)

	after, err := s.EventsSince(1, 1)
	require.NoError(t, err)
	require.Len(t, after, 1)
	assert.EqualValues(t, 2, after[0].ID)

	latest, err := s.LatestEvents(2)
	require.NoError(t, err)
	require.Len(t, latest, 2)
	assert.EqualValues(t, 2, latest[0].ID, "latest events come oldest first")
	assert.EqualValues(t, 3, latest[1].ID)
}

func TestSnapshotsAndState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(path)
	require.NoError(t, err)

	_, ok, err := s.Snapshot("a")
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, s.SaveSnapshot(monitor.Snapshot{Monitor: "a", Title: "old"}))
	require.NoError(t, s.SaveSnapshot(monitor.Snapshot{Monitor: "a", Title: "new"}))

	st := s.State("a")
	require.NoError(t, st.Set("seen", []string{"x"}))
	require.NoError(t, s.Close())

	// Everything survives a restart.
	s, err = Open(path)
	require.NoError(t, err)
	defer s.Close()
	snap, ok, err := s.Snapshot("a")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "new", snap.Title)

	var seen []string
	ok, err = s.State("a").Get("seen", &seen)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []string{"x"}, seen)

	ok, err = s.State("b").Get("seen", &seen)
	require.NoError(t, err)
	assert.False(t, ok, "state is per monitor")
}
