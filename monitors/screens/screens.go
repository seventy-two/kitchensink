// Package screens shows what's booked or on in kino's cinema and on
// boxset, the shared screens (separate apps on the same machine). With
// nothing booked, queued or playing it has nothing worth seeing, so it's
// sent as background.
package screens

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("screens", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// KinoURL is kino's own address, e.g. "http://127.0.0.1:6100";
	// empty leaves the cinema out.
	KinoURL string `json:"kinoUrl"`
	// KinoKey is kino's bot key (KINO_BOT_KEY), which may read the cinema.
	KinoKey string `json:"kinoKey"`
	// BoxsetURL is boxset's own address, e.g. "http://127.0.0.1:6160";
	// empty leaves boxset out. Boxset trusts the X-Player header there.
	BoxsetURL string `json:"boxsetUrl"`
	// BoxsetLink is the page a boxset row opens, e.g.
	// "https://0x48.io/boxset/".
	BoxsetLink string `json:"boxsetLink"`
	// Player is who to ask boxset as (default "M").
	Player string `json:"player"`
	// Timezone for showing times (default Europe/London).
	Timezone string `json:"timezone"`
}

// Monitor is the screens monitor.
type Monitor struct {
	cfg Config
	loc *time.Location
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.KinoURL == "" && c.BoxsetURL == "" {
		return nil, fmt.Errorf("screens needs a kinoUrl or a boxsetUrl")
	}
	if c.KinoURL != "" && c.KinoKey == "" {
		return nil, fmt.Errorf("screens needs kino's kinoKey to read the cinema")
	}
	c.KinoURL = strings.TrimRight(c.KinoURL, "/")
	c.BoxsetURL = strings.TrimRight(c.BoxsetURL, "/")
	if c.Player == "" {
		c.Player = "M"
	}
	if c.Timezone == "" {
		c.Timezone = "Europe/London"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	return &Monitor{cfg: c, loc: loc}, nil
}

// showing is the bit of kino's showing the panel uses.
type showing struct {
	URL  string `json:"url"`
	Film struct {
		Label string `json:"label"`
	} `json:"film"`
	BookedBy struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"bookedBy"`
	Starts     time.Time `json:"starts"`
	ASAP       bool      `json:"asap"`
	Status     string    `json:"status"`
	StatusText string    `json:"statusText"`
	Paused     bool      `json:"paused"`
	Late       bool      `json:"late"`
}

// item is the bit of boxset's queue item the panel uses.
type item struct {
	Show struct {
		Name string `json:"name"`
	} `json:"show"`
	Code       string `json:"code"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	StatusText string `json:"statusText"`
	Paused     bool   `json:"paused"`
	Watched    bool   `json:"watched"`
}

// waiting says a boxset item is still to come: queued, being fetched or
// ready, rather than finished, failed or removed.
func (it item) waiting() bool {
	switch it.Status {
	case "queued", "searching", "downloading", "preparing", "ready":
		return !it.Watched
	}
	return false
}

func (m *Monitor) cinema(ctx context.Context, client *http.Client) ([]showing, error) {
	var out struct {
		On []showing `json:"on"`
	}
	err := getJSON(ctx, client, m.cfg.KinoURL+"/api/cinema", map[string]string{"X-Kino-Key": m.cfg.KinoKey}, &out)
	if err != nil {
		return nil, fmt.Errorf("kino: %w", err)
	}
	return out.On, nil
}

func (m *Monitor) boxset(ctx context.Context, client *http.Client) ([]item, error) {
	var out struct {
		State struct {
			Items []item `json:"items"`
		} `json:"state"`
	}
	err := getJSON(ctx, client, m.cfg.BoxsetURL+"/api/state", map[string]string{"X-Player": m.cfg.Player}, &out)
	if err != nil {
		return nil, fmt.Errorf("boxset: %w", err)
	}
	return out.State.Items, nil
}

func getJSON(ctx context.Context, client *http.Client, u string, headers map[string]string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// row is one line of the panel.
type row struct {
	where, what, url, when string
	tone                   monitor.Tone
	live                   bool
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	var live, coming []row
	if m.cfg.KinoURL != "" {
		shows, err := m.cinema(ctx, env.HTTP)
		if err != nil {
			return monitor.Result{}, err
		}
		for _, sh := range shows {
			r := m.showingRow(sh, now)
			if r.live {
				live = append(live, r)
			} else {
				coming = append(coming, r)
			}
		}
	}
	if m.cfg.BoxsetURL != "" {
		items, err := m.boxset(ctx, env.HTTP)
		if err != nil {
			return monitor.Result{}, err
		}
		live, coming = m.boxsetRows(items, live, coming)
	}
	rows := append(live, coming...)

	table := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Where"}, {Title: "What", Fill: true}, {Title: "When", Align: monitor.AlignRight},
		},
		Rows: [][]monitor.Cell{},
	}
	var summary []monitor.Stat
	for _, r := range rows {
		table.Rows = append(table.Rows, []monitor.Cell{
			{Text: r.where, Tone: monitor.ToneMuted},
			{Text: r.what, URL: r.url},
			{Text: r.when, Tone: r.tone},
		})
		summary = append(summary, monitor.Stat{Label: r.where, Value: r.what, Detail: r.when, Tone: r.tone, URL: r.url})
	}
	res := monitor.Result{
		Blocks:       []monitor.Block{table},
		Summary:      summary,
		SummaryBlock: &table,
		Background:   len(rows) == 0,
	}
	if len(rows) == 0 {
		res.Blocks = []monitor.Block{{Type: monitor.BlockText, Text: "Nothing booked in the cinema or queued on boxset."}}
		res.SummaryBlock = nil
		res.Summary = []monitor.Stat{{Label: "Screens", Value: "Nothing on"}}
	}
	if len(live) > 0 {
		// Up top while something's on; the next poll renews it.
		res.Relevant = monitor.Window{Until: now.Add(5 * time.Minute)}
	}
	return res, nil
}

// showingRow is a cinema showing's line: live while it plays, else when
// it starts, with how getting it ready is going.
func (m *Monitor) showingRow(sh showing, now time.Time) row {
	r := row{where: "Cinema", what: sh.Film.Label, url: sh.URL}
	if by := either(sh.BookedBy.Name, sh.BookedBy.Username); by != "" {
		r.what += " · " + by
	}
	switch {
	case sh.Status == "playing" && sh.Paused:
		r.when, r.tone, r.live = "Paused", monitor.ToneWarn, true
	case sh.Status == "playing":
		r.when, r.tone, r.live = "Showing now", monitor.ToneGood, true
	default:
		r.when = "As soon as it's ready"
		if !sh.ASAP {
			r.when = m.day(sh.Starts, now)
		}
		if sh.Status != "scheduled" && sh.Status != "ready" {
			r.when += " · " + sh.StatusText
		}
		if sh.Late {
			r.tone = monitor.ToneWarn
		}
	}
	return r
}

// boxsetRows adds what's on boxset's screen, then the next episode in its
// queue, saying how many more are waiting.
func (m *Monitor) boxsetRows(items []item, live, coming []row) ([]row, []row) {
	var next []item
	for _, it := range items {
		if it.Status == "playing" {
			r := row{where: "Boxset", what: episode(it), url: m.cfg.BoxsetLink, when: "On now", tone: monitor.ToneGood, live: true}
			if it.Paused {
				r.when, r.tone = "Paused", monitor.ToneWarn
			}
			live = append(live, r)
		} else if it.waiting() {
			next = append(next, it)
		}
	}
	if len(next) > 0 {
		it := next[0]
		r := row{where: "Boxset", what: episode(it), url: m.cfg.BoxsetLink, when: it.StatusText}
		if len(next) > 1 {
			r.when = fmt.Sprintf("%s · %d more queued", it.StatusText, len(next)-1)
		}
		coming = append(coming, r)
	}
	return live, coming
}

func episode(it item) string {
	s := it.Show.Name
	if it.Code != "" {
		s += " " + it.Code
	}
	return s
}

// day says when a showing starts: "Today 20:00", "Tomorrow 20:00", or the
// weekday within the week, else the date.
func (m *Monitor) day(t, now time.Time) string {
	t, now = t.In(m.loc), now.In(m.loc)
	clock := t.Format("15:04")
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.UTC).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.UTC)).Hours() / 24)
	switch {
	case days <= 0:
		return "Today " + clock
	case days == 1:
		return "Tomorrow " + clock
	case days < 7:
		return t.Format("Mon") + " " + clock
	}
	return t.Format("2 Jan") + " " + clock
}

func either(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
