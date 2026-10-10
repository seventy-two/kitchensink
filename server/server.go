// Package server is kitchensink's HTTP API: snapshots, the event log and a
// server-sent events stream that clients keep open for live updates.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/engine"
	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/store"
)

// Engine is the part of engine.Engine the server uses.
type Engine interface {
	Snapshots() []monitor.Snapshot
	Refresh(id string) bool
	Subscribe() (<-chan engine.Message, func())
}

// Server handles the API.
type Server struct {
	Engine Engine
	Store  *store.Store
	// Token is the bearer token every /api request must carry.
	Token string
	Log   *slog.Logger
	// DefaultHeartbeat is how often the stream sends a keep-alive when the
	// client doesn't ask for something else.
	DefaultHeartbeat time.Duration
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("GET /api/monitors", s.auth(s.monitors))
	mux.Handle("POST /api/monitors/{id}/refresh", s.auth(s.refresh))
	mux.Handle("GET /api/events", s.auth(s.events))
	mux.Handle("GET /api/stream", s.auth(s.stream))
	return mux
}

func (s *Server) auth(h http.HandlerFunc) http.Handler {
	want := []byte("Bearer " + s.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if s.Token == "" || subtle.ConstantTimeCompare(got, want) != 1 {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func (s *Server) monitors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"monitors": s.Engine.Snapshots()})
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if !s.Engine.Refresh(r.PathValue("id")) {
		http.Error(w, "no such monitor", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// events returns events after ?since=ID, or the latest ones if since isn't
// given.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 100)
	if err != nil || limit < 1 || limit > 1000 {
		http.Error(w, "limit must be 1 to 1000", http.StatusBadRequest)
		return
	}
	var events []monitor.Event
	if r.URL.Query().Has("since") {
		since, err := intParam(r, "since", 0)
		if err != nil {
			http.Error(w, "bad since", http.StatusBadRequest)
			return
		}
		events, err = s.Store.EventsSince(int64(since), limit)
	} else {
		events, err = s.Store.LatestEvents(limit)
	}
	if err != nil {
		s.Log.Error("listing events", "err", err)
		http.Error(w, "listing events failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}

// maxReplay caps how many missed events a reconnecting client is sent.
const maxReplay = 500

// stream sends server-sent events: first any events after the client's last
// seen ID (from ?since= or Last-Event-ID), then every monitor's snapshot,
// then live updates. Comment lines keep the connection alive every
// ?heartbeat= seconds.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	since := int64(-1)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			http.Error(w, "bad Last-Event-ID", http.StatusBadRequest)
			return
		}
		since = n
	} else if r.URL.Query().Has("since") {
		n, err := intParam(r, "since", 0)
		if err != nil {
			http.Error(w, "bad since", http.StatusBadRequest)
			return
		}
		since = int64(n)
	}
	heartbeat := s.DefaultHeartbeat
	if heartbeat == 0 {
		heartbeat = 30 * time.Second
	}
	if r.URL.Query().Has("heartbeat") {
		n, err := intParam(r, "heartbeat", 0)
		if err != nil || n < 5 || n > 900 {
			http.Error(w, "heartbeat must be 5 to 900 seconds", http.StatusBadRequest)
			return
		}
		heartbeat = time.Duration(n) * time.Second
	}

	// Subscribe before replaying so nothing falls in the gap between them.
	msgs, unsubscribe := s.Engine.Subscribe()
	defer unsubscribe()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // stop nginx buffering the stream
	w.WriteHeader(http.StatusOK)

	var last int64
	if since >= 0 {
		missed, err := s.Store.EventsSince(since, maxReplay)
		if err != nil {
			s.Log.Error("replaying events", "err", err)
			return
		}
		for _, e := range missed {
			if writeEvent(w, e) != nil {
				return
			}
			last = e.ID
		}
	}
	for _, snap := range s.Engine.Snapshots() {
		if writeSSE(w, "", "snapshot", snap) != nil {
			return
		}
	}
	if writeSSE(w, "", "ready", map[string]int64{"last": last}) != nil || rc.Flush() != nil {
		return
	}

	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_, err = fmt.Fprint(w, ": ping\n\n")
		case m, ok := <-msgs:
			if !ok {
				// We fell behind; the client reconnects and catches up.
				return
			}
			switch m.Type {
			case engine.MessageEvent:
				if m.Event.ID <= last {
					continue // already sent in the replay
				}
				err = writeEvent(w, m.Event)
			case engine.MessageSnapshot:
				err = writeSSE(w, "", "snapshot", m.Snapshot)
			}
		}
		if err == nil {
			err = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e monitor.Event) error {
	return writeSSE(w, strconv.FormatInt(e.ID, 10), "event", e)
}

// writeSSE writes one server-sent event with a JSON payload. JSON has no raw
// newlines, so the payload fits on one data line.
func writeSSE(w http.ResponseWriter, id, name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var b strings.Builder
	if id != "" {
		b.WriteString("id: " + id + "\n")
	}
	b.WriteString("event: " + name + "\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	_, err = w.Write([]byte(b.String()))
	return err
}

func intParam(r *http.Request, name string, def int) (int, error) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, nil
	}
	return strconv.Atoi(v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
