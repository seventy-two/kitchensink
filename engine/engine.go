// Package engine runs monitors on their intervals, records what they find
// and passes it on to notifiers and connected clients.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/store"
)

// Notifier sends an event somewhere outside kitchensink, e.g. ntfy.
// Connected clients get events from Subscribe instead.
type Notifier interface {
	Notify(ctx context.Context, e monitor.Event) error
}

// Spec is one configured monitor.
type Spec struct {
	ID       string
	Kind     string
	Title    string
	Interval time.Duration
	Monitor  monitor.Monitor
}

// Options configures an Engine. Zero values get sensible defaults.
type Options struct {
	HTTP      *http.Client
	Log       *slog.Logger
	Notifiers []Notifier
	// PollTimeout bounds each poll. It defaults to two minutes.
	PollTimeout time.Duration
	// Now is the clock, for tests.
	Now func() time.Time
}

// MessageType says what a Message carries.
type MessageType string

const (
	MessageEvent    MessageType = "event"
	MessageSnapshot MessageType = "snapshot"
)

// Message is sent to subscribers when an event happens or a snapshot
// changes. Update marks an event that was already sent and has changed.
type Message struct {
	Type     MessageType
	Event    monitor.Event
	Snapshot monitor.Snapshot
	Update   bool
}

// minInterval stops a misbehaving monitor from polling in a tight loop.
const minInterval = 10 * time.Second

// Engine runs a set of monitors.
type Engine struct {
	store   *store.Store
	opts    Options
	runners []*runner
	byID    map[string]*runner
	hub     hub
}

type runner struct {
	spec    Spec
	refresh chan struct{}
	// busy keeps Poll and Act from running at once.
	busy sync.Mutex

	mu   sync.Mutex
	snap monitor.Snapshot
}

// New makes an engine for the given monitors. Snapshots saved by a previous
// run are loaded so clients have something to show before the first poll.
func New(st *store.Store, specs []Spec, opts Options) (*Engine, error) {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.PollTimeout == 0 {
		opts.PollTimeout = 2 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	e := &Engine{store: st, opts: opts, byID: map[string]*runner{}, hub: hub{subs: map[chan Message]struct{}{}}}
	for _, s := range specs {
		if _, dup := e.byID[s.ID]; dup {
			return nil, fmt.Errorf("monitor ID %q used twice", s.ID)
		}
		if s.Interval < minInterval {
			s.Interval = minInterval
		}
		r := &runner{spec: s, refresh: make(chan struct{}, 1)}
		snap, ok, err := st.Snapshot(s.ID)
		if err != nil {
			return nil, fmt.Errorf("loading snapshot for %s: %w", s.ID, err)
		}
		if !ok {
			snap = monitor.Snapshot{Blocks: []monitor.Block{}}
		}
		// The config may have changed since the snapshot was saved.
		snap.Monitor, snap.Kind, snap.Title = s.ID, s.Kind, s.Title
		r.snap = snap
		e.runners = append(e.runners, r)
		e.byID[s.ID] = r
	}
	return e, nil
}

// Run polls every monitor until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, r := range e.runners {
		wg.Go(func() { e.loop(ctx, r) })
	}
	wg.Wait()
}

func (e *Engine) loop(ctx context.Context, r *runner) {
	for {
		next := e.poll(ctx, r)
		t := time.NewTimer(next)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-r.refresh:
			t.Stop()
		}
	}
}

// poll runs one poll of r and returns how long to wait before the next.
func (e *Engine) poll(ctx context.Context, r *runner) time.Duration {
	log := e.opts.Log.With("monitor", r.spec.ID)
	pctx, cancel := context.WithTimeout(ctx, e.opts.PollTimeout)
	defer cancel()
	r.busy.Lock()
	res, err := r.spec.Monitor.Poll(pctx, e.env(r, log))
	r.busy.Unlock()
	if ctx.Err() != nil {
		return r.spec.Interval
	}

	r.mu.Lock()
	snap := r.snap
	if err != nil {
		log.Error("poll failed", "err", err)
		snap.Error = err.Error()
	} else {
		snap.Error = ""
		snap.Updated = e.opts.Now()
		snap.Blocks = res.Blocks
		snap.Summary = res.Summary
		snap.Subtitle = res.Subtitle
		snap.SummaryStyle = res.SummaryStyle
		snap.Timely = res.Timely
		snap.Background = res.Background
		snap.Paste = res.Paste
		if snap.Blocks == nil {
			snap.Blocks = []monitor.Block{}
		}
	}
	r.snap = snap
	r.mu.Unlock()

	if err := e.store.SaveSnapshot(snap); err != nil {
		log.Error("saving snapshot", "err", err)
	}
	e.hub.publish(Message{Type: MessageSnapshot, Snapshot: snap})

	for _, ev := range res.Events {
		e.record(ctx, log, r, ev)
	}

	if err != nil || res.Next <= 0 {
		return r.spec.Interval
	}
	return max(min(res.Next, r.spec.Interval), minInterval)
}

// record stores an event and, if it's new, passes it on.
func (e *Engine) record(ctx context.Context, log *slog.Logger, r *runner, ev monitor.Event) {
	ev.Monitor = r.spec.ID
	if ev.Time.IsZero() {
		ev.Time = e.opts.Now()
	}
	if ev.Priority == "" {
		ev.Priority = monitor.PriorityNormal
	}
	if ev.Update {
		// A change to an earlier event: passed on, never notified.
		ev.Updated = e.opts.Now()
		changed, ok, err := e.store.UpdateEvent(ev)
		if err != nil {
			log.Error("updating event", "key", ev.Key, "err", err)
		} else if ok {
			log.Info("event updated", "key", ev.Key, "title", changed.Title, "quiet", changed.Quiet)
			e.hub.publish(Message{Type: MessageEvent, Event: changed, Update: true})
		}
		return
	}
	ev, isNew, err := e.store.AddEvent(ev)
	if err != nil {
		log.Error("recording event", "key", ev.Key, "err", err)
		return
	}
	if !isNew {
		return
	}
	log.Info("event", "key", ev.Key, "title", ev.Title, "quiet", ev.Quiet)
	e.hub.publish(Message{Type: MessageEvent, Event: ev})
	if ev.Quiet {
		return
	}
	for _, n := range e.opts.Notifiers {
		nctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := n.Notify(nctx, ev); err != nil {
			log.Error("notifying", "key", ev.Key, "err", err)
		}
		cancel()
	}
}

// Snapshots returns every monitor's current snapshot, in config order.
func (e *Engine) Snapshots() []monitor.Snapshot {
	out := make([]monitor.Snapshot, 0, len(e.runners))
	for _, r := range e.runners {
		r.mu.Lock()
		out = append(out, r.snap)
		r.mu.Unlock()
	}
	return out
}

func (e *Engine) env(r *runner, log *slog.Logger) monitor.Env {
	return monitor.Env{
		State: e.store.State(r.spec.ID), HTTP: e.opts.HTTP, Now: e.opts.Now, Log: log,
		Act: func(ctx context.Context, id string, a monitor.Action) error {
			if id == r.spec.ID {
				// It's busy already, so this would never return.
				return fmt.Errorf("monitor %s can't act on itself", id)
			}
			return e.Act(ctx, id, a)
		},
	}
}

// Errors from Act.
var (
	ErrNoMonitor = errors.New("no such monitor")
	ErrNoActions = errors.New("monitor takes no actions")
)

// Act applies a client's change to a monitor, then has it poll so the
// change shows. Errors wrapping monitor.ErrBadAction are the client's.
func (e *Engine) Act(ctx context.Context, id string, a monitor.Action) error {
	r, ok := e.byID[id]
	if !ok {
		return ErrNoMonitor
	}
	actor, ok := r.spec.Monitor.(monitor.Actor)
	if !ok {
		return ErrNoActions
	}
	log := e.opts.Log.With("monitor", id)
	r.busy.Lock()
	err := actor.Act(ctx, e.env(r, log), a)
	r.busy.Unlock()
	if err != nil {
		return err
	}
	e.Refresh(id)
	return nil
}

// HookSecret is the secret a webhook to monitor id must carry, or "" if it
// takes none.
func (e *Engine) HookSecret(id string) string {
	if r, ok := e.byID[id]; ok {
		if h, ok := r.spec.Monitor.(monitor.Hooked); ok {
			return h.HookSecret()
		}
	}
	return ""
}

// Refresh asks a monitor to poll now. It reports false if there's no
// monitor with that ID.
func (e *Engine) Refresh(id string) bool {
	r, ok := e.byID[id]
	if !ok {
		return false
	}
	select {
	case r.refresh <- struct{}{}:
	default: // a refresh is already pending
	}
	return true
}

// Subscribe returns a channel of messages and a function to stop them. A
// subscriber that falls too far behind has its channel closed; it should
// reconnect and catch up from the store.
func (e *Engine) Subscribe() (<-chan Message, func()) {
	return e.hub.subscribe()
}

type hub struct {
	mu   sync.Mutex
	subs map[chan Message]struct{}
}

func (h *hub) subscribe() (<-chan Message, func()) {
	ch := make(chan Message, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *hub) publish(m Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- m:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}
