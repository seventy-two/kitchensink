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

// Hooked is an Actor that also takes webhooks from outside kitchensink,
// such as a game plugin that can't send the API token, at
// POST /api/hooks/{monitor}/{secret}. HookSecret is the secret the URL
// must carry (empty turns the hook off). Each hook reaches Act as an
// Action of type HookAction with the body's JSON as Data.
type Hooked interface {
	Actor
	HookSecret() string
}

// HookAction is the Action type a webhook arrives as.
const HookAction = "hook"

// Action is a change a client (or another monitor) asks for. Type says
// what, e.g. "add", "toggle" or "delete"; ID names the item, Text carries
// new text and Data anything more, as JSON.
type Action struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Text string          `json:"text,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
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
	// Act sends an action to another monitor by its ID, as a client
	// would, e.g. to add an event to the calendar. It's nil where there's
	// no engine, as in -check. A monitor must not act on itself.
	Act func(ctx context.Context, monitor string, a Action) error
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
	// SummaryBlock, if set, is drawn in place of Summary by clients that
	// can, e.g. the first few rows of the panel's table, so the panel
	// looks the same short. Keep Summary too for those that can't.
	SummaryBlock *Block
	// Timely says when it was updated matters at the moment, e.g. for
	// prices or a game in progress. Clients only show its age then.
	Timely bool
	// Relevant, if set, is when the panel matters most, e.g. while a game's
	// on or just before a meeting: clients with room put it beside their
	// event history then. From may be zero, for now; Until is exclusive.
	Relevant Window
	// Background says the monitor works for others (the mail monitor
	// fills the calendar), so clients leave it off the dashboard and show
	// it with their settings, for diagnosing it.
	Background bool
	// Paste, if set, gives the panel a button that sends a "paste" action
	// with the clipboard's text, e.g. a game's share text for the dailies.
	// It's the button's tooltip.
	Paste string
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
	// Update, from a monitor, changes the title, body and quiet of the
	// event already recorded with this key, e.g. a price move that's since
	// changed, rather than recording a new one. It never notifies, and does
	// nothing if there's no such event.
	Update bool `json:"-"`
	// Updated is when the event was last changed by an update, for clients
	// to replace the copy they have.
	Updated time.Time `json:"updated,omitzero"`
	// Expires, if set, makes the event temporary: worth seeing only until
	// then, like the outcome of something the user just did. Clients show
	// temporary events apart from the history while they last, then drop
	// them.
	Expires time.Time `json:"expires,omitzero"`
}

// Snapshot is the current state of one monitor, as clients render it.
type Snapshot struct {
	Monitor string    `json:"monitor"`
	Kind    string    `json:"kind"`
	Title   string    `json:"title"`
	Updated time.Time `json:"updated"`
	// Error is the last poll's error, if it failed. Blocks then still hold
	// the last good data.
	Error    string `json:"error,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	// Timely is the last good poll's Result.Timely.
	Timely bool `json:"timely,omitempty"`
	// Relevant is the last good poll's Result.Relevant.
	Relevant Window `json:"relevant,omitzero"`
	// Background is the last good poll's Result.Background.
	Background bool `json:"background,omitempty"`
	// Paste is the last good poll's Result.Paste.
	Paste   string  `json:"paste,omitempty"`
	Blocks  []Block `json:"blocks"`
	Summary []Stat  `json:"summary,omitempty"`
	// SummaryStyle is how to draw Summary; empty for plain tiles.
	SummaryStyle SummaryStyle `json:"summaryStyle,omitempty"`
	// SummaryBlock is the last good poll's Result.SummaryBlock.
	SummaryBlock *Block `json:"summaryBlock,omitempty"`
}

// Window is a span of time; the zero Window is none.
type Window struct {
	From  time.Time `json:"from,omitzero"`
	Until time.Time `json:"until,omitzero"`
}

// Contains reports whether t is in the window.
func (w Window) Contains(t time.Time) bool {
	return !w.Until.IsZero() && t.Before(w.Until) && !t.Before(w.From)
}

// SummaryStyle is how clients draw a summary.
type SummaryStyle string

const (
	// SummaryTiles, the default, draws each stat as a label, value and
	// detail.
	SummaryTiles SummaryStyle = ""
	// SummaryChips draws each stat as a small block filled with its
	// tone's colour: the label, the value, and the detail if any. It suits
	// many short values, like a watchlist's daily moves or an hourly
	// forecast.
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
	// a "toggle" action with its ID, and clients offer "delete" (labelled
	// with DeleteHint if set); if AddHint
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
	// DeleteHint says what a checklist's delete button does when that
	// isn't deleting the item, e.g. "Stop tracking".
	DeleteHint string `json:"deleteHint,omitempty"`
	// NoDelete leaves out a checklist's delete button.
	NoDelete bool `json:"noDelete,omitempty"`
	// Movable gives a checklist's items buttons that send "move" actions
	// with Text "up" or "down". UpHint and DownHint, if set, say what each
	// does when it isn't moving the item in the list (e.g. "Put off a
	// day").
	Movable  bool   `json:"movable,omitempty"`
	UpHint   string `json:"upHint,omitempty"`
	DownHint string `json:"downHint,omitempty"`
	// Private marks personal figures, e.g. money, which clients can hide
	// on request.
	Private bool `json:"private,omitempty"`
}

// Stat is a labelled value in a stats block.
type Stat struct {
	Label  string `json:"label"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
	Tone   Tone   `json:"tone,omitempty"`
	// Private marks a personal figure, which clients can hide on request.
	Private bool `json:"private,omitempty"`
	// Series, if set, is drawn as a sparkline.
	Series []float64 `json:"series,omitempty"`
	// Icon names a small picture to draw by the value. Clients know the
	// weather's: sun, moon, sun-cloud, moon-cloud, cloud, fog, drizzle,
	// rain, showers, snow and storm, and ignore names they don't know.
	Icon string `json:"icon,omitempty"`
	// Level, from 0 to 1, is drawn faintly behind the stat as a chart
	// joined to its neighbours', e.g. the chance of rain each hour.
	// Nil for none: 0 still draws, along the bottom.
	Level *float64 `json:"level,omitempty"`
	// Lead sets the stat apart from the ones after it, e.g. the
	// portfolio's day ahead of the watchlist's moves.
	Lead bool `json:"lead,omitempty"`
	// URL, if set, is opened when the stat is clicked.
	URL string `json:"url,omitempty"`
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
	// Local, if set, is a button for an app on the client's own machine,
	// e.g. a downloader. Clients that don't have the app draw nothing.
	Local *Local `json:"local,omitempty"`
	// Action, if set, is a button that sends the monitor an action (see
	// Actor), drawn in place of the cell's text.
	Action *CellAction `json:"action,omitempty"`
}

// CellAction is a table cell's button: clicked, the client sends the
// monitor an Action of this Type and ID. Label says what it does, e.g. for
// a tooltip.
type CellAction struct {
	Type  string `json:"type"`
	ID    string `json:"id,omitempty"`
	Label string `json:"label"`
}

// Local is something a client can do with an app installed beside it,
// rather than through the server: run App with Arg.
type Local struct {
	App string `json:"app"`
	Arg string `json:"arg"`
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
