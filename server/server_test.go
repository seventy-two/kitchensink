package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/engine"
	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEngine struct {
	snaps     []monitor.Snapshot
	refreshed []string
	actions   []monitor.Action
	msgs      chan engine.Message
}

func (f *fakeEngine) Act(ctx context.Context, id string, a monitor.Action) error {
	f.actions = append(f.actions, a)
	switch {
	case id == "stocks":
		return engine.ErrNoActions
	case id != "todo":
		return engine.ErrNoMonitor
	case a.Type == "explode":
		return errors.New("disk full")
	case a.Type != "add":
		return fmt.Errorf("%w: unknown type", monitor.ErrBadAction)
	}
	return nil
}

func (f *fakeEngine) Snapshots() []monitor.Snapshot { return f.snaps }
func (f *fakeEngine) Refresh(id string) bool {
	f.refreshed = append(f.refreshed, id)
	return id == "stocks"
}
func (f *fakeEngine) Subscribe() (<-chan engine.Message, func()) { return f.msgs, func() {} }

func setup(t *testing.T) (*httptest.Server, *fakeEngine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { st.Close() })
	for _, k := range []string{"a", "b", "c"} {
		_, _, err := st.AddEvent(monitor.Event{Monitor: "stocks", Key: k, Title: "event " + k})
		require.NoError(t, err)
	}
	eng := &fakeEngine{
		snaps: []monitor.Snapshot{{Monitor: "stocks", Title: "Watchlist", Blocks: []monitor.Block{}}},
		msgs:  make(chan engine.Message, 10),
	}
	s := &Server{Engine: eng, Store: st, Token: "secret", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, eng, st
}

func do(t *testing.T, method, url, token string) *http.Response {
	return doBody(t, method, url, token, "")
}

func doBody(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	require.NoError(t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAuth(t *testing.T) {
	srv, _, _ := setup(t)
	assert.Equal(t, http.StatusUnauthorized, do(t, "GET", srv.URL+"/api/monitors", "").StatusCode)
	assert.Equal(t, http.StatusUnauthorized, do(t, "GET", srv.URL+"/api/monitors", "wrong").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, "GET", srv.URL+"/api/monitors", "secret").StatusCode)
	assert.Equal(t, http.StatusOK, do(t, "GET", srv.URL+"/healthz", "").StatusCode, "health needs no token")
}

func TestMonitorsAndRefresh(t *testing.T) {
	srv, eng, _ := setup(t)
	var body struct{ Monitors []monitor.Snapshot }
	require.NoError(t, json.NewDecoder(do(t, "GET", srv.URL+"/api/monitors", "secret").Body).Decode(&body))
	require.Len(t, body.Monitors, 1)
	assert.Equal(t, "Watchlist", body.Monitors[0].Title)

	assert.Equal(t, http.StatusAccepted, do(t, "POST", srv.URL+"/api/monitors/stocks/refresh", "secret").StatusCode)
	assert.Equal(t, http.StatusNotFound, do(t, "POST", srv.URL+"/api/monitors/nope/refresh", "secret").StatusCode)
	assert.Equal(t, []string{"stocks", "nope"}, eng.refreshed)
}

func TestEvents(t *testing.T) {
	srv, _, _ := setup(t)
	get := func(query string) []string {
		resp := do(t, "GET", srv.URL+"/api/events"+query, "secret")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct{ Events []monitor.Event }
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		var keys []string
		for _, e := range body.Events {
			keys = append(keys, e.Key)
		}
		return keys
	}
	assert.Equal(t, []string{"a", "b", "c"}, get(""))
	assert.Equal(t, []string{"b", "c"}, get("?limit=2"), "latest events")
	assert.Equal(t, []string{"b"}, get("?since=1&limit=1"))
	assert.Equal(t, http.StatusBadRequest, do(t, "GET", srv.URL+"/api/events?limit=0", "secret").StatusCode)
}

// sse reads server-sent events from a stream.
type sse struct{ r *bufio.Reader }

type sseEvent struct{ id, name, data string }

func (s sse) next(t *testing.T) sseEvent {
	t.Helper()
	var e sseEvent
	for {
		line, err := s.r.ReadString('\n')
		require.NoError(t, err)
		line = strings.TrimSuffix(line, "\n")
		switch {
		case line == "":
			if e.name != "" {
				return e
			}
		case strings.HasPrefix(line, ":"):
			e.name = "comment"
			return e
		case strings.HasPrefix(line, "id: "):
			e.id = line[4:]
		case strings.HasPrefix(line, "event: "):
			e.name = line[7:]
		case strings.HasPrefix(line, "data: "):
			e.data = line[6:]
		}
	}
}

func TestStream(t *testing.T) {
	srv, eng, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream?since=1&heartbeat=5", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	s := sse{bufio.NewReader(resp.Body)}

	// Missed events, then snapshots, then ready.
	e := s.next(t)
	assert.Equal(t, sseEvent{"2", "event", e.data}, e)
	assert.Contains(t, e.data, `"key":"b"`)
	assert.Equal(t, "3", s.next(t).id)
	e = s.next(t)
	assert.Equal(t, "snapshot", e.name)
	assert.Contains(t, e.data, `"title":"Watchlist"`)
	e = s.next(t)
	assert.Equal(t, "ready", e.name)
	assert.JSONEq(t, `{"last":3}`, e.data)

	// Live: an event already replayed is skipped, a new one comes through.
	eng.msgs <- engine.Message{Type: engine.MessageEvent, Event: monitor.Event{ID: 3, Key: "c"}}
	eng.msgs <- engine.Message{Type: engine.MessageEvent, Event: monitor.Event{ID: 4, Key: "d"}}
	eng.msgs <- engine.Message{Type: engine.MessageSnapshot, Snapshot: monitor.Snapshot{Monitor: "stocks"}}
	e = s.next(t)
	assert.Equal(t, "4", e.id)
	assert.Equal(t, "snapshot", s.next(t).name)
}

func TestStreamUpdates(t *testing.T) {
	srv, eng, st := setup(t)
	// Event a changed after the client saw event b.
	_, _, err := st.UpdateEvent(monitor.Event{Monitor: "stocks", Key: "a", Title: "event a, changed", Updated: time.Now()})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream?since=2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	s := sse{bufio.NewReader(resp.Body)}

	assert.Equal(t, "3", s.next(t).id, "the missed event")
	e := s.next(t)
	assert.Equal(t, "1", e.id, "then the changed one, to replace")
	assert.Contains(t, e.data, `"title":"event a, changed"`)
	assert.Equal(t, "snapshot", s.next(t).name)
	assert.Equal(t, "ready", s.next(t).name)

	// Live, a change to an event already sent comes through.
	eng.msgs <- engine.Message{Type: engine.MessageEvent, Event: monitor.Event{ID: 2, Key: "b", Title: "b changed"}, Update: true}
	e = s.next(t)
	assert.Equal(t, "2", e.id)
	assert.Contains(t, e.data, "b changed")
}

func TestStreamParams(t *testing.T) {
	srv, _, _ := setup(t)
	assert.Equal(t, http.StatusBadRequest, do(t, "GET", srv.URL+"/api/stream?heartbeat=1", "secret").StatusCode)
	assert.Equal(t, http.StatusBadRequest, do(t, "GET", srv.URL+"/api/stream?since=x", "secret").StatusCode)
}

func TestStreamLastEventID(t *testing.T) {
	srv, _, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/stream", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Last-Event-ID", "2")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "3", sse{bufio.NewReader(resp.Body)}.next(t).id)
}

func TestActions(t *testing.T) {
	srv, eng, _ := setup(t)
	post := func(id, body string) int {
		return doBody(t, "POST", srv.URL+"/api/monitors/"+id+"/actions", "secret", body).StatusCode
	}
	assert.Equal(t, http.StatusNoContent, post("todo", `{"type":"add","text":"Bins"}`))
	assert.Equal(t, monitor.Action{Type: "add", Text: "Bins"}, eng.actions[0])
	assert.Equal(t, http.StatusBadRequest, post("todo", `{"type":"fly"}`), "the monitor refuses it")
	assert.Equal(t, http.StatusBadRequest, post("todo", `{"type":"add","colour":"red"}`), "unknown fields")
	assert.Equal(t, http.StatusBadRequest, post("todo", `not json`))
	assert.Equal(t, http.StatusNotFound, post("nope", `{"type":"add"}`))
	assert.Equal(t, http.StatusMethodNotAllowed, post("stocks", `{"type":"add"}`), "takes no actions")
	assert.Equal(t, http.StatusInternalServerError, post("todo", `{"type":"explode"}`))
	assert.Equal(t, http.StatusUnauthorized, doBody(t, "POST", srv.URL+"/api/monitors/todo/actions", "", `{"type":"add"}`).StatusCode)
}
