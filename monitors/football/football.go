// Package football shows the NFL and the user's fantasy teams as one
// panel: the fantasy matchups, then a few of the day's games, chosen for
// the followed teams and the user's players (see nfl.Monitor.Prefer). The
// two monitors run as they would on their own, each with its own share of
// the state, and their events are both raised.
package football

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitors/fantasy"
	"github.com/seventy-two/kitchensink/monitors/nfl"
)

func init() {
	monitor.Register("football", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config: each part's own, as the nfl and fantasy
// monitors take it.
type Config struct {
	NFL     json.RawMessage `json:"nfl"`
	Fantasy json.RawMessage `json:"fantasy"`
	// Games is how many games the summary shows at most. It defaults to 4.
	Games int `json:"games"`
}

// Monitor is the football monitor.
type Monitor struct {
	nfl     *nfl.Monitor
	fantasy *fantasy.Monitor
	games   int
	// The last good result of each, kept for when the other's poll fails.
	lastNFL, lastFantasy *monitor.Result
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Games: 4}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	n, err := nfl.New(c.NFL)
	if err != nil {
		return nil, err
	}
	f, err := fantasy.New(c.Fantasy)
	if err != nil {
		return nil, err
	}
	return &Monitor{nfl: n, fantasy: f, games: c.Games}, nil
}

// State key prefixes: each part's keys are kept apart.
const (
	nflPrefix     = "nfl/"
	fantasyPrefix = "fantasy/"
)

// Poll polls fantasy first, for the teams the user's players are on, then
// the NFL. If one fails, its last good result stands in; only if both
// fail does the poll.
func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	fe := env
	fe.State = prefixed{env.State, fantasyPrefix}
	f, ferr := m.fantasy.Poll(ctx, fe)
	if ferr == nil {
		m.lastFantasy = &f
	} else {
		env.Log.Warn("fantasy poll failed", "err", ferr)
	}

	m.nfl.Prefer(m.fantasy.Teams(), m.games)
	ne := env
	ne.State = prefixed{env.State, nflPrefix}
	n, nerr := m.nfl.Poll(ctx, ne)
	if nerr == nil {
		m.lastNFL = &n
	} else {
		env.Log.Warn("nfl poll failed", "err", nerr)
	}

	if ferr != nil && nerr != nil {
		return monitor.Result{}, errors.Join(ferr, nerr)
	}
	var out monitor.Result
	out.SummaryStyle = monitor.SummaryChips
	// Events only from this poll's results; the stand-ins' were raised.
	if ferr == nil {
		out.Events = append(out.Events, f.Events...)
	}
	if nerr == nil {
		out.Events = append(out.Events, n.Events...)
	}
	for _, r := range []*monitor.Result{m.lastFantasy, m.lastNFL} {
		if r == nil {
			continue
		}
		out.Blocks = append(out.Blocks, r.Blocks...)
		out.Summary = append(out.Summary, r.Summary...)
		out.Timely = out.Timely || r.Timely
		if r.Next > 0 && (out.Next == 0 || r.Next < out.Next) {
			out.Next = r.Next
		}
	}
	return out, nil
}

// prefixed is a monitor's state with its keys under a prefix.
type prefixed struct {
	s      monitor.State
	prefix string
}

func (p prefixed) Get(key string, v any) (bool, error) { return p.s.Get(p.prefix+key, v) }
func (p prefixed) Set(key string, v any) error         { return p.s.Set(p.prefix+key, v) }
