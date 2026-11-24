// Package codes passes on one-time sign-in codes (2FA, verification) that
// trashbox, the user's mail client, finds in new mail: each one becomes a
// high-priority event, so it reaches the phone and the PC.
//
// Codes are short-lived and sensitive, so the monitor is careful with them:
// they're held only in memory (never in the monitor's saved state), and
// once a code's time is up its event is rewritten without it, so it doesn't
// stay in the event history either. The list of events to wipe (key, sender
// and time, never the code) is saved, so a restart can't leave one behind. Clients know code events by their key
// ("code:...") and keep the code off the phone's lock screen.
//
// trashbox posts {"from","code","account"} to the hook,
// /api/hooks/{monitor}/{secret}.
package codes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("codes", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// HookSecret is the secret in the hook URL trashbox posts to. Use
	// "${ENV_VAR}".
	HookSecret string `json:"hookSecret"`
	// Keep is how long a code is shown before it's wiped; 10m by default.
	Keep monitor.Duration `json:"keep"`
}

// Monitor holds the codes of the last few minutes.
type Monitor struct {
	secret string
	keep   time.Duration

	mu     sync.Mutex
	codes  []*entry             // not yet raised
	recent map[string]time.Time // sender+code → when, to spot trashbox's retries (memory only)
}

type entry struct {
	key, from, code string
	at              time.Time
}

// wipe is an event whose code must be removed at Ends. It's saved state,
// so it holds no code.
type wipe struct {
	Key  string    `json:"key"`
	From string    `json:"from"`
	At   time.Time `json:"at"`
	Ends time.Time `json:"ends"`
}

const wipesKey = "wipes"

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Keep: monitor.Duration(10 * time.Minute)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if len(c.HookSecret) < 16 {
		return nil, fmt.Errorf("hookSecret must be at least 16 characters")
	}
	if c.Keep <= 0 {
		return nil, fmt.Errorf("keep must be positive")
	}
	return &Monitor{secret: c.HookSecret, keep: time.Duration(c.Keep)}, nil
}

// HookSecret is the secret trashbox's hook URL carries.
func (m *Monitor) HookSecret() string { return m.secret }

// post is what trashbox sends.
type post struct {
	From    string `json:"from"`
	Code    string `json:"code"`
	Account string `json:"account"`
}

var validCode = regexp.MustCompile(`^[A-Za-z0-9-]{4,12}$`)

// Act takes a code from the hook.
func (m *Monitor) Act(_ context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != monitor.HookAction {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
	}
	var p post
	if err := json.Unmarshal(a.Data, &p); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	if !validCode.MatchString(p.Code) {
		return fmt.Errorf("%w: not a code", monitor.ErrBadAction)
	}
	from := strings.TrimSpace(p.From)
	if r := []rune(from); len(r) > 60 {
		from = string(r[:60])
	}
	if from == "" {
		from = "an email"
	}
	id := make([]byte, 8)
	rand.Read(id)
	now := env.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.recent == nil {
		m.recent = map[string]time.Time{}
	}
	for k, at := range m.recent {
		if now.Sub(at) >= m.keep {
			delete(m.recent, k)
		}
	}
	seen := from + "\x00" + p.Code
	if _, ok := m.recent[seen]; ok {
		return monitor.ErrQuiet // the same code again (e.g. trashbox retried)
	}
	m.recent[seen] = now
	m.codes = append(m.codes, &entry{key: "code:" + hex.EncodeToString(id), from: from, code: p.Code, at: now})
	return nil
}

// Poll raises an event for each new code, and wipes the code from each
// event once its time is up.
func (m *Monitor) Poll(_ context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	res := monitor.Result{Background: true}
	var wipes []wipe
	if _, err := env.State.Get(wipesKey, &wipes); err != nil {
		return res, err
	}

	// New codes: an event each, and a note to wipe it.
	m.mu.Lock()
	fresh := m.codes
	m.codes = nil
	m.mu.Unlock()
	for _, e := range fresh {
		ends := e.at.Add(m.keep)
		if !now.Before(ends) {
			continue // arrived too late to be worth showing
		}
		res.Events = append(res.Events, monitor.Event{
			Key: e.key, Time: e.at, Priority: monitor.PriorityHigh,
			Title: "Code from " + e.from, Body: e.code, Expires: ends,
		})
		wipes = append(wipes, wipe{Key: e.key, From: e.from, At: e.at, Ends: ends})
	}

	// Wipe what's due; keep the rest for later.
	left := wipes[:0]
	var next time.Duration
	for _, w := range wipes {
		if !now.Before(w.Ends) {
			res.Events = append(res.Events, monitor.Event{
				Key: w.Key, Time: w.At, Update: true, Quiet: true,
				Title: "Code from " + w.From + " (expired)", Body: "",
			})
			continue
		}
		if d := w.Ends.Sub(now); next == 0 || d < next {
			next = d
		}
		left = append(left, w)
	}
	if err := env.State.Set(wipesKey, left); err != nil {
		return res, err
	}
	if next > 0 {
		res.Next = next + time.Second
	}
	res.Blocks = []monitor.Block{{Type: monitor.BlockText, Text: fmt.Sprintf("%d code(s) live", len(left))}}
	return res, nil
}
