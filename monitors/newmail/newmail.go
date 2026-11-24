// Package newmail shows the unread mail of the last two days from trashbox,
// the user's mail client, which posts the whole list to the hook whenever it
// changes (new mail, mail read anywhere) and every 10 minutes anyway.
//
// trashbox posts {"mail":[{"key","account","from","subject","date","url"}]}
// to /api/hooks/{monitor}/{secret}. Each url opens the account's webmail
// and carries ?tb=<id>, so a client with trashbox installed opens the
// message there instead (Cell.Open).
package newmail

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("newmail", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// HookSecret is the secret in the hook URL trashbox posts to. Use
	// "${ENV_VAR}".
	HookSecret string `json:"hookSecret"`
	// Stale is how long without hearing from trashbox before the panel
	// says the list may be out of date; 30m by default (trashbox posts
	// every 10 minutes while it runs).
	Stale monitor.Duration `json:"stale"`
	// Timezone for the times shown; Europe/London by default.
	Timezone string `json:"timezone"`
}

// Monitor holds the latest list.
type Monitor struct {
	secret string
	stale  time.Duration
	loc    *time.Location
}

// Mail is one message as trashbox sends it.
type Mail struct {
	Key     string    `json:"key"`
	Account string    `json:"account"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
	URL     string    `json:"url"`
}

// saved is the monitor's state.
type saved struct {
	Mail []Mail    `json:"mail"`
	At   time.Time `json:"at"` // when trashbox last posted
	// Told is the keys already raised as events (so a restart, or mail
	// read and marked unread again, doesn't raise them twice).
	Told []string `json:"told"`
}

const stateKey = "inbox"

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Stale: monitor.Duration(30 * time.Minute), Timezone: "Europe/London"}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if len(c.HookSecret) < 16 {
		return nil, fmt.Errorf("hookSecret must be at least 16 characters")
	}
	if c.Stale <= 0 {
		return nil, fmt.Errorf("stale must be positive")
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	return &Monitor{secret: c.HookSecret, stale: time.Duration(c.Stale), loc: loc}, nil
}

// HookSecret is the secret trashbox's hook URL carries.
func (m *Monitor) HookSecret() string { return m.secret }

// Act takes the list from the hook.
func (m *Monitor) Act(_ context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != monitor.HookAction {
		return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
	}
	var p struct {
		Mail []Mail `json:"mail"`
	}
	if err := json.Unmarshal(a.Data, &p); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	if len(p.Mail) > 100 {
		p.Mail = p.Mail[:100]
	}
	var list []Mail
	for _, x := range p.Mail {
		if x.Key == "" {
			continue
		}
		x.From, x.Subject, x.Account = clip(x.From, 80), clip(x.Subject, 200), clip(x.Account, 100)
		if u, err := url.Parse(x.URL); err != nil || u.Scheme != "https" {
			x.URL = ""
		}
		list = append(list, x)
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Date.After(list[j].Date) })

	var st saved
	if _, err := env.State.Get(stateKey, &st); err != nil {
		return err
	}
	same := len(st.Mail) == len(list)
	for i := 0; same && i < len(list); i++ {
		same = st.Mail[i] == list[i]
	}
	st.Mail, st.At = list, env.Now()
	if err := env.State.Set(stateKey, st); err != nil {
		return err
	}
	if same {
		return monitor.ErrQuiet // trashbox's 10-minute "still here"
	}
	return nil
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Poll shows the list and raises a quiet event for each message it hasn't
// before.
func (m *Monitor) Poll(_ context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	var st saved
	if _, err := env.State.Get(stateKey, &st); err != nil {
		return monitor.Result{}, err
	}
	res := monitor.Result{Next: 5 * time.Minute}

	told := map[string]bool{}
	for _, k := range st.Told {
		told[k] = true
	}
	for i := len(st.Mail) - 1; i >= 0; i-- { // oldest first, so Recent reads in order
		x := st.Mail[i]
		if told[x.Key] {
			continue
		}
		told[x.Key] = true
		st.Told = append(st.Told, x.Key)
		res.Events = append(res.Events, monitor.Event{
			Key: "mail:" + x.Key, Time: x.Date, Title: "Mail from " + x.From, Body: x.Subject, URL: x.URL,
			Priority: monitor.PriorityLow, Quiet: true, // a list to glance at, not a ping each
		})
	}
	if len(st.Told) > 500 {
		st.Told = st.Told[len(st.Told)-500:]
	}
	if len(res.Events) > 0 {
		if err := env.State.Set(stateKey, st); err != nil {
			return res, err
		}
	}

	if len(st.Mail) == 0 {
		res.Background = true // nothing new: off the dashboard
		res.Blocks = []monitor.Block{{Type: monitor.BlockText, Text: "Nothing unread from the last two days."}}
	} else {
		rows := make([][]monitor.Cell, 0, len(st.Mail))
		for _, x := range st.Mail {
			subject := x.Subject
			if subject == "" {
				subject = "(no subject)"
			}
			rows = append(rows, []monitor.Cell{
				{Text: x.From, Detail: x.Account},
				{Text: subject, URL: x.URL, Open: "trashbox"},
				{Text: m.when(x.Date, now), Tone: monitor.ToneMuted},
			})
		}
		res.Blocks = []monitor.Block{{
			Type: monitor.BlockTable,
			Columns: []monitor.Column{
				{Title: "From"}, {Title: "Subject", Fill: true}, {Title: "When", Align: monitor.AlignRight},
			},
			Rows: rows,
		}}
		res.Summary = []monitor.Stat{{Label: "Unread", Value: fmt.Sprint(len(st.Mail)), Tone: monitor.ToneAccent}}
	}
	if !st.At.IsZero() && now.Sub(st.At) > m.stale {
		res.Subtitle = "As of " + m.when(st.At, now) + ": trashbox hasn't been in touch since"
	}
	return res, nil
}

// when is "14:05" today, "Mon 14:05" within the week, else "24 Nov".
func (m *Monitor) when(t, now time.Time) string {
	t, now = t.In(m.loc), now.In(m.loc)
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	days := int(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC).Sub(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	switch {
	case days <= 0:
		return t.Format("15:04")
	case days < 7:
		return t.Format("Mon 15:04")
	default:
		return t.Format("2 Jan")
	}
}
