// Package claude shows how much of the user's Claude plan is left: the
// five-hour session, the week, and any weekly limit for one model (Fable).
// kitchensink can't read it itself, as that needs the user's Claude login,
// so the claude-usage command on their PC sends the plan's limits as
// "usage" actions (see cmd/claude-usage) and this monitor keeps the latest.
package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("claude", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// Stale is how old a reading can be before the panel says so; 30
	// minutes by default.
	Stale monitor.Duration `json:"stale"`
}

// Monitor keeps the latest usage reading.
type Monitor struct {
	stale time.Duration
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Stale: monitor.Duration(30 * time.Minute)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.Stale <= 0 {
		return nil, fmt.Errorf("stale must be positive")
	}
	return &Monitor{stale: time.Duration(c.Stale)}, nil
}

// Limit is one entry of the "limits" list in Claude's usage response
// (GET api.anthropic.com/api/oauth/usage, the numbers behind /usage).
type Limit struct {
	// Kind is "session", "weekly_all" or "weekly_scoped".
	Kind     string    `json:"kind"`
	Percent  float64   `json:"percent"`
	Severity string    `json:"severity"` // "normal", "warning", …
	ResetsAt time.Time `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// Label names the limit for the panel.
func (l Limit) Label() string {
	if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
		return l.Scope.Model.DisplayName
	}
	switch l.Kind {
	case "session":
		return "Session"
	case "weekly_all":
		return "Week"
	}
	return strings.ReplaceAll(l.Kind, "_", " ")
}

// reading is what's kept: the limits and when they were read.
type reading struct {
	Limits []Limit   `json:"limits"`
	Read   time.Time `json:"read"`
}

const readingKey = "reading"

var london = func() *time.Location {
	if l, err := time.LoadLocation("Europe/London"); err == nil {
		return l
	}
	return time.UTC
}()

func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != "usage" {
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	var body struct {
		Limits []Limit `json:"limits"`
	}
	if err := json.Unmarshal(a.Data, &body); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	if len(body.Limits) == 0 || len(body.Limits) > 10 {
		return fmt.Errorf("%w: want 1-10 limits, got %d", monitor.ErrBadAction, len(body.Limits))
	}
	return env.State.Set(readingKey, reading{Limits: body.Limits, Read: env.Now()})
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	var r reading
	ok, err := env.State.Get(readingKey, &r)
	if err != nil {
		return monitor.Result{}, err
	}
	if !ok {
		return monitor.Result{Blocks: []monitor.Block{{Type: monitor.BlockText,
			Text: "No reading yet: claude-usage sends one after Claude Code replies on the PC."}}}, nil
	}
	now := env.Now()
	stats := Stats(r.Limits, now)
	res := monitor.Result{
		Blocks:   []monitor.Block{{Type: monitor.BlockStats, Stats: stats}},
		Summary:  stats,
		Subtitle: "Read " + Ago(now.Sub(r.Read)),
		Timely:   true,
	}
	if now.Sub(r.Read) > m.stale {
		res.Subtitle += " (Claude Code hasn't run since)"
	}
	// Repoll when the next limit resets, so it shows as reset on time.
	for _, l := range r.Limits {
		if d := l.ResetsAt.Sub(now); d > 0 && (res.Next == 0 || d < res.Next) {
			res.Next = d + time.Second
		}
	}
	return res, nil
}

// Stats turns limits into one stat each: the percentage used, toned by how
// close it is, and when it resets. A limit whose reset has passed shows as
// 0%, as nothing has been used since as far as anyone's said.
func Stats(ls []Limit, now time.Time) []monitor.Stat {
	var out []monitor.Stat
	for _, l := range ls {
		s := monitor.Stat{Label: l.Label()}
		pct := l.Percent
		if !l.ResetsAt.IsZero() && !now.Before(l.ResetsAt) {
			pct = 0
			s.Detail = "reset"
		} else if !l.ResetsAt.IsZero() {
			s.Detail = "resets " + When(l.ResetsAt, now)
		}
		s.Value = strconv.Itoa(int(pct+0.5)) + "%"
		switch {
		case pct >= 90:
			s.Tone = monitor.ToneBad
		case pct >= 75 || (pct > 0 && l.Severity != "" && l.Severity != "normal"):
			s.Tone = monitor.ToneWarn
		}
		out = append(out, s)
	}
	return out
}

// When says when t is, from now: a clock time today, otherwise the day too.
func When(t, now time.Time) string {
	t, now = t.In(london), now.In(london)
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	if t.Sub(now) < 6*24*time.Hour {
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

// Ago says how long ago something was, roughly.
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d ago"
}
