// Package monitor defines what a kitchensink monitor is: something polled on
// an interval that returns a dashboard snapshot and any events worth a
// notification.
//
// Each monitor kind lives in its own package and registers a Factory from an
// init function, so adding one is a matter of writing the package and
// importing it from main.
package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Monitor watches one thing. Poll is never called concurrently for the same
// monitor.
type Monitor interface {
	Poll(ctx context.Context, env Env) (Result, error)
}

// Actor is a monitor that clients can change, such as a todo list. The
// engine never calls Act at the same time as Poll, and polls straight
// after, so the change shows at once.
type Actor interface {
	Act(ctx context.Context, env Env, a Action) error
}

// Action is a change a client asks for. Type says what, e.g. "add",
// "toggle" or "delete"; ID names the item and Text carries new text.
type Action struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Text string `json:"text,omitempty"`
}

// ErrBadAction wraps the error for an action a monitor can't do, such as
// an unknown type or an item that doesn't exist; the API answers 400.
var ErrBadAction = errors.New("bad action")

// Env is what the engine gives a monitor on each poll.
type Env struct {
	// State persists small values between polls and restarts.
	State State
	// HTTP is the client to fetch with.
	HTTP *http.Client
	// Now is the current time; tests replace it.
	Now func() time.Time
	Log *slog.Logger
}

// State is a monitor's own key-value store. Values are JSON encoded.
type State interface {
	// Get decodes the value at key into v and reports whether it existed.
	Get(key string, v any) (bool, error)
	Set(key string, v any) error
}

// Result is the outcome of one poll.
type Result struct {
	// Blocks make up the monitor's dashboard panel.
	Blocks []Block
	// Subtitle says what the monitor is watching when that isn't obvious,
	// e.g. a car search's filters. Clients show it above the panel.
	Subtitle string
	// Summary is the few things most worth seeing at a glance, e.g. the
	// next meeting or the biggest movers. Clients show it in place of the
	// blocks when space is short.
	Summary []Stat
	// SummaryStyle says how clients draw the summary.
	SummaryStyle SummaryStyle
	// Events are things that happened. The engine drops any whose Key the
	// monitor has emitted before, so monitors can re-emit freely.
	Events []Event
	// Next brings the following poll forward when it's sooner than the
	// configured interval, e.g. to poll often during a live game. Zero or
	// anything longer keeps the interval.
	Next time.Duration
}

// Priority says how loudly a client should notify.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// Event is something worth telling the user about.
type Event struct {
	// ID is assigned by the store; it orders events and lets clients catch
	// up on what they missed.
	ID int64 `json:"id"`
	// Monitor is the ID of the monitor that raised it, set by the engine.
	Monitor string `json:"monitor"`
	// Key identifies the event within its monitor, for de-duplication.
	Key      string    `json:"key"`
	Time     time.Time `json:"time"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	URL      string    `json:"url,omitempty"`
	Image    string    `json:"image,omitempty"`
	Priority Priority  `json:"priority"`
	// Quiet events are logged and shown in clients' history but don't
	// raise a notification, e.g. adverts already listed on the first check.
	Quiet bool `json:"quiet,omitempty"`
}

// Snapshot is the current state of one monitor, as clients render it.
type Snapshot struct {
	Monitor string    `json:"monitor"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	// Error is the last poll's error, if it failed. Blocks then still hold
	// the last good data.
	Error    string  `json:"error,omitempty"`
	Subtitle string  `json:"subtitle,omitempty"`
	Blocks   []Block `json:"blocks"`
	Summary  []Stat  `json:"summary,omitempty"`
	// SummaryStyle is how to draw Summary; empty for plain tiles.
	SummaryStyle SummaryStyle `json:"summaryStyle,omitempty"`
}

// SummaryStyle is how clients draw a summary.
type SummaryStyle string

const (
	// SummaryTiles, the default, draws each stat as a label, value and
	// detail.
	SummaryTiles SummaryStyle = ""
	// SummaryChips draws each stat as a small block filled with its
	// tone's colour, with the label above the value; details are left out.
	// It suits many short values, like a watchlist's daily moves.
	SummaryChips SummaryStyle = "chips"
)

// BlockType says how a Block is drawn. Clients draw every monitor with the
// same few block types, so a new monitor needs no client changes.
type BlockType string

const (
	// BlockStats is a row of labelled values, optionally with sparklines.
	BlockStats BlockType = "stats"
	// BlockTable is a table of Columns and Rows.
	BlockTable BlockType = "table"
	// BlockList is a list of Items, each optionally with a link and image.
	BlockList BlockType = "list"
	// BlockText is a paragraph of plain text.
	BlockText BlockType = "text"
	// BlockChecklist is a list of Items with checkboxes. Ticking one sends
	// a "toggle" action with its ID, and clients offer "delete"; if AddHint
	// is set, they also offer a box for "add" actions.
	BlockChecklist BlockType = "checklist"
)

// Tone colours a value: good and bad are green and red, and so on. The empty
// tone is the default text colour.
type Tone string

const (
	ToneNone   Tone = ""
	ToneGood   Tone = "good"
	ToneBad    Tone = "bad"
	ToneWarn   Tone = "warn"
	ToneMuted  Tone = "muted"
	ToneAccent Tone = "accent"
)

// Block is one part of a dashboard panel. Only the fields for its Type are
// set.
type Block struct {
	Type    BlockType `json:"type"`
	Title   string    `json:"title,omitempty"`
	Stats   []Stat    `json:"stats,omitempty"`
	Columns []Column  `json:"columns,omitempty"`
	Rows    [][]Cell  `json:"rows,omitempty"`
	Items   []Item    `json:"items,omitempty"`
	Text    string    `json:"text,omitempty"`
	// AddHint is the placeholder for a checklist's box for new items.
	AddHint string `json:"addHint,omitempty"`
}

// Stat is a labelled value in a stats block.
type Stat struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Tone   Tone   `json:"tone,omitempty"`
	// Series, if set, is drawn as a sparkline.
	Series []float64 `json:"series,omitempty"`
}

// Align is a table column's alignment.
type Align string

const (
	AlignLeft   Align = "left"
	AlignRight  Align = "right"
	AlignCenter Align = "center"
)

// Column is a table column heading.
type Column struct {
	Title string `json:"title"`
	Align Align  `json:"align,omitempty"`
	// Fill gives the column the table's spare width. Clients otherwise
	// pick the left-aligned column with the most text.
	Fill bool `json:"fill,omitempty"`
}

// Cell is a table cell.
type Cell struct {
	Text string `json:"text"`
	Tone Tone   `json:"tone,omitempty"`
	URL  string `json:"url,omitempty"`
	// Detail is a smaller, muted line under the text.
	Detail string `json:"detail,omitempty"`
}

// Item is an entry in a list or checklist block.
type Item struct {
	// ID and Done are for checklists.
	ID       string `json:"id,omitempty"`
	Done     bool   `json:"done,omitempty"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Detail   string `json:"detail,omitempty"`
	URL      string `json:"url,omitempty"`
	Image    string `json:"image,omitempty"`
	Tone     Tone   `json:"tone,omitempty"`
}

// Duration is a time.Duration written in JSON as a string such as "5m".
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"5m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// Factory builds a monitor from its JSON config.
type Factory func(config json.RawMessage) (Monitor, error)

var (
	mu        sync.RWMutex
	factories = map[string]Factory{}
)

// Register makes a monitor kind available to config files. It panics if the
// kind is registered twice, which is a programming error.
func Register(kind string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := factories[kind]; dup {
		panic("monitor: kind registered twice: " + kind)
	}
	factories[kind] = f
}

// New builds a monitor of the given kind.
func New(kind string, config json.RawMessage) (Monitor, error) {
	mu.RLock()
	f, ok := factories[kind]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown monitor type %q (known: %v)", kind, Kinds())
	}
	return f(config)
}

// Kinds lists the registered monitor kinds.
func Kinds() []string {
	mu.RLock()
	defer mu.RUnlock()
	kinds := make([]string, 0, len(factories))
	for k := range factories {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

// Decode decodes a monitor's config into v, rejecting unknown fields so
// typos in the config file are caught at startup. Empty config leaves v as
// it is, so callers can set defaults first.
func Decode(config json.RawMessage, v any) error {
	if len(bytes.TrimSpace(config)) == 0 || string(bytes.TrimSpace(config)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(config))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decoding config: %w", err)
	}
	return nil
}
