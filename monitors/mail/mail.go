// Package mail reads email for what belongs elsewhere on the dashboard:
// bookings, tickets and appointments go on the calendar, things an email
// asks you to do by a date go on the to-do list, orders tick off chores
// (placing the weekly shop) and things bought regularly become chores to
// reorder them. It shows no mail itself.
//
// Messages are picked out by subject first (orders, bookings, deliveries
// and the like), and only those are passed to Gemini, which reads each
// and says what, if anything, to add.
package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitors/groceries"
)

func init() {
	monitor.Register("mail", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	Accounts []Account `json:"accounts"`
	Gemini   Gemini    `json:"gemini"`
	// Timezone is the IANA zone of dates in email; it defaults to
	// Europe/London.
	Timezone string `json:"timezone"`
	// LookbackDays is how far back to read the first time, which finds
	// what's still to come and how often things are reordered. It
	// defaults to 365.
	LookbackDays int `json:"lookbackDays"`
	// Calendar, Todo and Chores are the IDs of the monitors to add to;
	// they default to "calendar", "todo" and "chores". Empty leaves one
	// out.
	Calendar *string `json:"calendar"`
	Todo     *string `json:"todo"`
	Chores   *string `json:"chores"`
	// ChoresDone maps a chore's ID to the kind of email that means it's
	// been done, e.g. {"waitrose-order": "a Waitrose groceries order
	// being placed"}.
	ChoresDone map[string]string `json:"choresDone"`
	// Reorders turns things bought again and again into chores to reorder
	// them. It defaults to true.
	Reorders *bool `json:"reorders"`
	// Groceries is the ID of the monitor that tracks food from grocery
	// deliveries; it defaults to "groceries". GroceryReceipts says which
	// emails are a delivery's receipt (listing what came), to pass on to
	// it with their attachments; none are unless it's set.
	Groceries       *string   `json:"groceries"`
	GroceryReceipts []Receipt `json:"groceryReceipts"`
	// IgnoreFrom skips mail from senders whose address contains any of
	// these, e.g. a newsletter whose subjects look like orders.
	IgnoreFrom []string `json:"ignoreFrom"`
}

// Receipt picks out emails by sender and subject: each matches if empty,
// or if the sender's address (or subject) contains it, ignoring case.
type Receipt struct {
	From    string `json:"from"`
	Subject string `json:"subject"`
}

// Account is a mailbox to read.
type Account struct {
	// Type is "imap" (e.g. Gmail with an app password) or "outlook" (a
	// Microsoft account, signed in from the dashboard).
	Type string `json:"type"`
	// Name labels it, e.g. "Gmail"; it's also where its progress is kept,
	// so changing it reads the mailbox again.
	Name string `json:"name"`

	// For imap: the server as host:port (default imap.gmail.com:993),
	// the user, the password (an app password; "${NAME}" keeps it in the
	// environment) and the folder (default INBOX).
	Server   string `json:"server"`
	User     string `json:"user"`
	Password string `json:"password"`
	Folder   string `json:"folder"`

	// For outlook: the application (client) ID of an app registered in
	// Microsoft Entra that allows public client flows, and its tenant
	// (default "consumers", for personal accounts).
	ClientID string `json:"clientId"`
	Tenant   string `json:"tenant"`
}

// Monitor is the mail reader.
type Monitor struct {
	accounts   []account
	gemini     *gemini
	loc        *time.Location
	lookback   int
	calendar   string
	todo       string
	chores     string
	choresDone map[string]string
	reorders   bool
	ignoreFrom []string
	groceries  string
	receipts   []Receipt
	perPoll    int // emails read each poll
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Timezone: "Europe/London", LookbackDays: 365}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone: %w", err)
	}
	if c.LookbackDays < 1 {
		return nil, fmt.Errorf("lookbackDays must be at least 1")
	}
	g, err := newGemini(c.Gemini)
	if err != nil {
		return nil, err
	}
	pick := func(p *string, def string) string {
		if p == nil {
			return def
		}
		return *p
	}
	m := &Monitor{
		gemini: g, loc: loc, lookback: c.LookbackDays,
		calendar: pick(c.Calendar, "calendar"), todo: pick(c.Todo, "todo"), chores: pick(c.Chores, "chores"),
		groceries: pick(c.Groceries, "groceries"), receipts: c.GroceryReceipts,
		choresDone: c.ChoresDone, reorders: c.Reorders == nil || *c.Reorders, ignoreFrom: c.IgnoreFrom, perPoll: readPerPoll,
	}
	if len(c.Accounts) == 0 {
		return nil, fmt.Errorf("no accounts")
	}
	names := map[string]bool{}
	for _, a := range c.Accounts {
		if a.Name == "" {
			return nil, fmt.Errorf("an account needs a name")
		}
		if names[a.Name] {
			return nil, fmt.Errorf("two accounts are called %q", a.Name)
		}
		names[a.Name] = true
		var acc account
		switch a.Type {
		case "imap":
			acc, err = newIMAP(a)
		case "outlook":
			acc, err = newOutlook(a)
		default:
			err = fmt.Errorf("unknown type %q", a.Type)
		}
		if err != nil {
			return nil, fmt.Errorf("account %s: %w", a.Name, err)
		}
		m.accounts = append(m.accounts, acc)
	}
	return m, nil
}

// account is a mailbox kitchensink can read.
type account interface {
	Name() string
	// Open connects. It returns a *signIn error while the account waits
	// for someone to sign in.
	Open(ctx context.Context, env monitor.Env) (session, error)
}

// session is an open mailbox.
type session interface {
	// List returns the messages after cursor, oldest first, or for a new
	// cursor ("") those since a time, and the cursor to pass next time.
	List(cursor string, since time.Time) ([]header, string, error)
	// Body is a message's text, by its Ref.
	Body(ref string) (string, error)
	// Files are a message's attachments, by its Ref.
	Files(ref string) ([]groceries.File, error)
	Close() error
}

// header is what's known of a message before reading it.
type header struct {
	Account string    `json:"account"`
	ID      string    `json:"id"`  // its Message-ID, to spot copies
	Ref     string    `json:"ref"` // how its account fetches it
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

// signIn is the error from Open while an account waits to be signed in.
type signIn struct {
	URL, Code string
	Expires   time.Time
	// Wait is how soon to look again.
	Wait time.Duration
}

func (s *signIn) Error() string { return "waiting for sign-in" }

// State keys.
const (
	cursorsKey  = "cursors"  // per account
	failedKey   = "failed"   // Gemini failures, by message ID
	queueKey    = "queue"    // headers picked out but not yet read
	seenKey     = "seen"     // message IDs read, with the day
	foundKey    = "found"    // recent findings, newest first
	boughtKey   = "bought"   // days each routine item was bought
	accountsKey = "accounts" // each account's last check
)

// Limits on each poll's work, so it fits in the engine's poll timeout;
// a backlog carries on in quick polls.
const (
	readPerPoll  = 10
	readBudget   = 50 * time.Second // no new email is started after this, so a poll fits in two minutes
	backlogPoll  = 15 * time.Second
	retryPoll    = time.Minute // after Gemini fails
	maxAttempts  = 4           // Gemini failures before an email is skipped
	keepFound    = 30
	keepSeenDays = 400
)

// status is how an account's last check went.
type status struct {
	Checked time.Time `json:"checked,omitzero"`
	Error   string    `json:"error,omitempty"`
	SignIn  *signIn   `json:"signIn,omitempty"`
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	var (
		cursors  = map[string]string{}
		queue    []header
		seen     = map[string]string{}
		statuses = map[string]status{}
	)
	for k, v := range map[string]any{cursorsKey: &cursors, queueKey: &queue, seenKey: &seen, accountsKey: &statuses} {
		if _, err := env.State.Get(k, v); err != nil {
			return monitor.Result{}, fmt.Errorf("reading %s: %w", k, err)
		}
	}
	queued := map[string]bool{}
	for _, h := range queue {
		queued[h.ID] = true
	}

	// List what's new in each account and queue what looks worth reading.
	var events []monitor.Event
	sessions := map[string]session{}
	defer func() {
		for _, s := range sessions {
			s.Close()
		}
	}()
	next := time.Duration(0)
	soonest := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}
	for _, a := range m.accounts {
		st := status{Checked: statuses[a.Name()].Checked}
		s, err := a.Open(ctx, env)
		if si, ok := errors.AsType[*signIn](err); ok {
			st.SignIn = si
			soonest(si.Wait)
			events = append(events, monitor.Event{
				Key:   "signin:" + a.Name() + ":" + now.In(m.loc).Format(time.DateOnly),
				Title: "Sign in to " + a.Name(),
				Body:  fmt.Sprintf("kitchensink needs to read your mail: go to %s and enter %s", si.URL, si.Code),
				URL:   si.URL,
			})
		} else if err != nil {
			st.Error = err.Error()
			env.Log.Warn("opening mailbox", "account", a.Name(), "err", err)
		} else {
			sessions[a.Name()] = s
			since := now.AddDate(0, 0, -m.lookback)
			hs, cur, err := s.List(cursors[a.Name()], since)
			if err != nil {
				st.Error = err.Error()
				env.Log.Warn("listing mail", "account", a.Name(), "err", err)
			} else {
				for _, h := range hs {
					h.Account = a.Name()
					if seen[h.ID] != "" || queued[h.ID] || !m.worthReading(h) {
						continue
					}
					queue = append(queue, h)
					queued[h.ID] = true
				}
				cursors[a.Name()] = cur
				st.Checked = now
			}
		}
		statuses[a.Name()] = st
	}
	slices.SortStableFunc(queue, func(a, b header) int { return a.Date.Compare(b.Date) })

	// Read the oldest few, so what was bought before is known before
	// what's bought again.
	found, err := m.loadFound(env)
	if err != nil {
		return monitor.Result{}, err
	}
	failed := map[string]int{}
	if _, err := env.State.Get(failedKey, &failed); err != nil {
		return monitor.Result{}, err
	}
	var readErr string
	read := 0
	started := time.Now()
	for len(queue) > 0 && read < m.perPoll && time.Since(started) < readBudget && ctx.Err() == nil {
		h := queue[0]
		s := sessions[h.Account]
		if s == nil {
			if !slices.ContainsFunc(m.accounts, func(a account) bool { return a.Name() == h.Account }) {
				queue = queue[1:] // the account's gone from the config
				continue
			}
			break // can't read it now; keep the order
		}
		text, err := s.Body(h.Ref)
		if err != nil {
			// Most likely deleted since; don't let it block the rest.
			env.Log.Warn("reading message", "account", h.Account, "subject", h.Subject, "err", err)
			seen[h.ID] = now.Format(time.DateOnly)
			queue = queue[1:]
			continue
		}
		read++
		// A grocery receipt goes to the groceries monitor, whatever
		// Gemini makes of it here; that reads each order once.
		passed := ""
		if m.isReceipt(h) && now.Sub(h.Date) < receiptMaxAge {
			passed = m.passReceipt(ctx, env, s, h, text)
		}
		x, err := m.gemini.extract(ctx, env.HTTP, m.prompt(h, text, now))
		if err != nil {
			env.Log.Warn("extracting from mail", "subject", h.Subject, "err", err)
			// Out of credit, say: no fault of this email's, so it keeps
			// its place (three went unread on 24 Oct 2026 that way).
			if errors.Is(err, errUnavailable) {
				readErr = "Gemini: " + err.Error()
				break
			}
			if failed[h.ID]++; failed[h.ID] >= maxAttempts {
				// This one email keeps failing (too long, say): skip it
				// rather than hold up the rest.
				env.Log.Warn("skipping mail Gemini can't read", "subject", h.Subject)
				delete(failed, h.ID)
				seen[h.ID] = now.Format(time.DateOnly)
				queue = queue[1:]
				continue
			}
			readErr = "Gemini: " + err.Error()
			break // try again soon
		}
		delete(failed, h.ID)
		f := m.apply(ctx, env, h, x, now)
		if passed != "" {
			f.Did = append(f.Did, passed)
		}
		events = append(events, f.notify...)
		if len(f.Did) > 0 {
			// An email read again replaces what it found before.
			found = append([]finding{f}, slices.DeleteFunc(found, func(o finding) bool { return o.ID == f.ID })...)
			if now.Sub(h.Date) < 48*time.Hour {
				events = append(events, monitor.Event{
					Key: "found:" + h.ID, Title: f.Summary, Body: strings.Join(f.Did, " · "), Quiet: true, Priority: monitor.PriorityLow,
				})
			}
		}
		seen[h.ID] = now.Format(time.DateOnly)
		queue = queue[1:]
	}
	if len(found) > keepFound {
		found = found[:keepFound]
	}
	cutoff := now.AddDate(0, 0, -keepSeenDays).Format(time.DateOnly)
	maps.DeleteFunc(seen, func(_, day string) bool { return day < cutoff })

	for k, v := range map[string]any{cursorsKey: cursors, queueKey: queue, seenKey: seen, accountsKey: statuses, foundKey: found, failedKey: failed} {
		if err := env.State.Set(k, v); err != nil {
			return monitor.Result{}, fmt.Errorf("saving %s: %w", k, err)
		}
	}
	switch {
	case readErr != "":
		soonest(retryPoll)
	case len(queue) > 0:
		soonest(backlogPoll)
	}
	signingIn := false
	for _, st := range statuses {
		signingIn = signingIn || st.SignIn != nil
	}
	return monitor.Result{
		// It only feeds other panels, unless an account needs signing in.
		Background: !signingIn,
		Blocks:     m.blocks(statuses, queue, found, readErr, now),
		Summary:    m.summary(statuses, queue, found),
		Events:     events,
		Next:       next,
	}, nil
}

// worthReading picks out messages whose subject looks like an order,
// booking or appointment, from senders not ignored.
func (m *Monitor) worthReading(h header) bool {
	from := strings.ToLower(h.From)
	for _, s := range m.ignoreFrom {
		if s != "" && strings.Contains(from, strings.ToLower(s)) {
			return false
		}
	}
	return looksUseful(h.Subject)
}

// receiptMaxAge is how old a grocery receipt can be to pass on: older
// ones read while catching up are eaten by now.
const receiptMaxAge = 7 * 24 * time.Hour

// isReceipt reports whether a message is a grocery delivery's receipt.
func (m *Monitor) isReceipt(h header) bool {
	if m.groceries == "" {
		return false
	}
	from, subject := strings.ToLower(h.From), strings.ToLower(h.Subject)
	for _, r := range m.receipts {
		if (r.From != "" || r.Subject != "") &&
			strings.Contains(from, strings.ToLower(r.From)) && strings.Contains(subject, strings.ToLower(r.Subject)) {
			return true
		}
	}
	return false
}

// passReceipt sends a receipt, with its PDFs, to the groceries monitor,
// and says so.
func (m *Monitor) passReceipt(ctx context.Context, env monitor.Env, s session, h header, text string) string {
	files, err := s.Files(h.Ref)
	if err != nil {
		env.Log.Warn("reading a receipt's attachments", "subject", h.Subject, "err", err)
	}
	files = slices.DeleteFunc(files, func(f groceries.File) bool { return f.Type != "application/pdf" })
	data, _ := json.Marshal(groceries.Order{Email: h.ID, From: h.From, Subject: h.Subject, Received: h.Date, Text: text, Files: files})
	a := monitor.Action{Type: groceries.OrderAction, Data: data}
	if err := act(ctx, env, m.groceries, a); err != nil {
		logAct(env.Log, m.groceries, monitor.Action{Type: a.Type, ID: h.Subject}, err)
		return ""
	}
	return "Groceries: order passed on"
}

// finding is what one email added.
type finding struct {
	ID      string    `json:"id,omitempty"` // the email's
	Time    time.Time `json:"time"`
	Account string    `json:"account"`
	From    string    `json:"from"`
	Summary string    `json:"summary"`
	Did     []string  `json:"did"`
	// notify is what to tell them now, such as a parcel having come.
	notify []monitor.Event
}

func (m *Monitor) loadFound(env monitor.Env) ([]finding, error) {
	var found []finding
	if _, err := env.State.Get(foundKey, &found); err != nil {
		return nil, fmt.Errorf("reading findings: %w", err)
	}
	return found, nil
}

// act sends an action to another monitor, if it's configured and there's
// an engine to send it.
func act(ctx context.Context, env monitor.Env, id string, a monitor.Action) error {
	if id == "" || env.Act == nil {
		return errors.New("not connected")
	}
	return env.Act(ctx, id, a)
}

// logAct logs an action that failed.
func logAct(log *slog.Logger, to string, a monitor.Action, err error) {
	log.Warn("passing on from mail", "to", to, "action", a.Type, "id", a.ID, "err", err)
}
