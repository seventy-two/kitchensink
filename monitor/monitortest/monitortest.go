// Package monitortest helps test monitors: an in-memory State, an Env built
// on it, and a server that replays a saved API response.
package monitortest

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// State is an in-memory monitor.State.
type State struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *State) Get(key string, v any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.values[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func (s *State) Set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[key] = b
	return nil
}

// Env returns an Env with fresh in-memory state and a clock fixed at now.
func Env(now time.Time) monitor.Env {
	return monitor.Env{
		State: &State{},
		HTTP:  http.DefaultClient,
		Now:   func() time.Time { return now },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// Request is a request the fixture server received.
type Request struct {
	Method, Path, Query string
	Header              http.Header
	Body                []byte
}

// Fixture is a test server that answers every request with a file's
// contents and records what it was asked.
type Fixture struct {
	*httptest.Server
	mu       sync.Mutex
	body     []byte
	status   int
	requests []Request
}

// Serve starts a Fixture serving the file at path. It's closed when the
// test ends.
func Serve(t testing.TB, path string) *Fixture {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &Fixture{body: body, status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: b})
		status, body := f.status, f.body
		f.mu.Unlock()
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(f.Close)
	return f
}

// Reply changes what the server answers with.
func (f *Fixture) Reply(status int, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

// Requests returns the requests received so far.
func (f *Fixture) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

// Keys lists events' keys, for compact assertions.
func Keys(events []monitor.Event) []string {
	keys := []string{}
	for _, e := range events {
		keys = append(keys, e.Key)
	}
	return keys
}
