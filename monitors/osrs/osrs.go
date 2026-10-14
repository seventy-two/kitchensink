// Package osrs shows what an Old School RuneScape player has done today
// towards pets: boss kills, and XP in the skills that have a pet, from
// Wise Old Man (https://docs.wiseoldman.net/), plus big drops and pets.
// RuneLite's Dink plugin, sending to the monitor's hook, makes it live (see
// dink.go).
package osrs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("osrs", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const (
	defaultWOM = "https://api.wiseoldman.net/v2"
	userAgent  = "kitchensink (github.com/seventy-two/kitchensink)"
)

// Config is the monitor's config.
type Config struct {
	// Player is the OSRS name, as Wise Old Man tracks it.
	Player string `json:"player"`
	// Timezone is where the day starts at midnight. It defaults to
	// Europe/London.
	Timezone string `json:"timezone"`
	// HookSecret turns on the hook for Dink's webhooks, at
	// /api/hooks/{monitor}/{secret}. Use "${ENV_VAR}".
	HookSecret string `json:"hookSecret"`
	// BigLoot is the least a drop must be worth (in coins) to show and
	// notify. It defaults to 20m.
	BigLoot int64 `json:"bigLoot"`
	// WOMURL replaces Wise Old Man's API, for tests.
	WOMURL string `json:"womURL"`
}

// womEvery is how often Wise Old Man is asked to update the player, at
// most. The hiscores only change when the player logs out, and Dink's
// hooks (which have the monitor poll after every kill) cover the time in
// between, so an hourly update does.
const womEvery = 55 * time.Minute

// Monitor is the OSRS monitor.
type Monitor struct {
	cfg Config
	loc *time.Location

	// The player's stats from WOM, and when.
	wom   stats
	womAt time.Time
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.Player == "" {
		return nil, fmt.Errorf("osrs needs a player")
	}
	if c.Timezone == "" {
		c.Timezone = "Europe/London"
	}
	if c.BigLoot == 0 {
		c.BigLoot = 20_000_000
	}
	if c.WOMURL == "" {
		c.WOMURL = defaultWOM
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	return &Monitor{cfg: c, loc: loc}, nil
}

// stats are a snapshot's numbers by WOM metric: XP, kills or score.
// Unranked ones (WOM's -1, e.g. under a boss's hiscore minimum) are 0.
type stats map[string]int64

// baseline is the day's starting point, kept for the day.
type baseline struct {
	Day   string `json:"day"`
	Stats stats  `json:"stats"`
}

const baselineKey = "baseline"

// Poll asks Wise Old Man to update the player, then compares their stats
// with the last snapshot from before midnight. WOM only snapshots when
// asked, so the poll just after midnight (see Next) is what marks the
// day's start; without one, the last earlier snapshot does.
func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now().In(m.loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.loc)
	day := midnight.Format(time.DateOnly)

	if m.wom == nil || env.Now().Sub(m.womAt) >= womEvery {
		wom, err := m.update(ctx, env.HTTP)
		if err != nil {
			return monitor.Result{}, err
		}
		m.wom, m.womAt = wom, env.Now()
	}
	current := m.wom
	var base baseline
	if _, err := env.State.Get(baselineKey, &base); err != nil {
		return monitor.Result{}, err
	}
	if base.Day != day {
		start, err := m.before(ctx, env.HTTP, midnight.Add(5*time.Minute))
		if err != nil {
			return monitor.Result{}, err
		}
		if start == nil {
			start = current // nothing earlier: today starts now
		}
		base = baseline{Day: day, Stats: start}
		if err := env.State.Set(baselineKey, base); err != nil {
			return monitor.Result{}, err
		}
	}

	// What Dink has sent is newer than WOM's, until WOM catches up.
	var live, lastBig stats
	var td today
	if _, err := env.State.Get(liveKey, &live); err != nil {
		return monitor.Result{}, err
	}
	if _, err := env.State.Get(lastBigKey, &lastBig); err != nil {
		return monitor.Result{}, err
	}
	if _, err := env.State.Get(todayKey, &td); err != nil {
		return monitor.Result{}, err
	}
	from := base.Stats
	if td.Day == day && len(td.Starts) > 0 {
		from = merged(base.Stats, td.Starts)
	}
	rows := gains(from, merged(current, live))
	drops, events := dropsToday(td, day)

	blocks := []monitor.Block{table(rows, lastBig, m.cfg.BigLoot)}
	if len(drops) > 0 {
		blocks = append(blocks, dropTable(drops, m.loc))
	}
	// Short, the panel is its first table cut to the top few rows.
	short := blocks[0]
	if len(rows) == 0 && len(drops) > 0 {
		short = blocks[1]
	}
	short.Rows = short.Rows[:min(len(short.Rows), shortRows)]
	return monitor.Result{
		Blocks:       blocks,
		Summary:      append(dropChips(drops), summary(rows, len(drops) > 0)...),
		SummaryStyle: monitor.SummaryChips,
		SummaryBlock: &short,
		Events:       events,
		Next:         midnight.AddDate(0, 0, 1).Add(time.Minute).Sub(now),
	}, nil
}

// merged is a with each of b's numbers that's higher.
func merged(a, b stats) stats {
	out := stats{}
	for id, n := range a {
		out[id] = n
	}
	for id, n := range b {
		out[id] = max(out[id], n)
	}
	return out
}

// dropChips are a chip for each of today's pets and big drops.
func dropChips(drops []drop) []monitor.Stat {
	var out []monitor.Stat
	for _, dr := range drops {
		if dr.Pet {
			out = append(out, monitor.Stat{Label: "Pet", Value: petName(dr), Tone: monitor.ToneAccent})
		} else {
			out = append(out, monitor.Stat{Label: dr.Item, Value: gp(dr.Value), Tone: monitor.ToneGood, URL: wiki(strings.SplitN(dr.Item, " ×", 2)[0])})
		}
	}
	return out
}

// shortRows is how many rows the panel shows when space is short.
const shortRows = 3

// gain is one metric's progress today.
type gain struct {
	metric
	ID            string
	Gained, Total int64
}

// gains are the tracked metrics that went up: kills and scores first,
// then XP, each most first.
func gains(from, to stats) []gain {
	var out []gain
	for id, mt := range tracked {
		if d := to[id] - from[id]; d > 0 {
			out = append(out, gain{metric: mt, ID: id, Gained: d, Total: to[id]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Kind == xp) != (b.Kind == xp) {
			return b.Kind == xp
		}
		if a.Gained != b.Gained {
			return a.Gained > b.Gained
		}
		return a.Name < b.Name
	})
	return out
}

// table lists the day's gains, with the pet's drop rate and, for bosses
// that have given a big drop or a pet, the kills since the last one.
func table(rows []gain, lastBig stats, bigLoot int64) monitor.Block {
	if len(rows) == 0 {
		return monitor.Block{Type: monitor.BlockText, Text: "Nothing towards a pet yet today."}
	}
	b := monitor.Block{Type: monitor.BlockTable, Columns: []monitor.Column{
		{Title: ""}, {Title: "Today", Align: monitor.AlignRight}, {Title: "Total", Align: monitor.AlignRight},
		{Title: "Pet", Align: monitor.AlignRight}, {Title: "Since " + gp(bigLoot) + "+", Align: monitor.AlignRight},
	}}
	for _, g := range rows {
		since := ""
		if last, ok := lastBig[g.ID]; ok && g.Kind != xp && g.Total >= last {
			since = comma(g.Total - last)
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			{Text: g.Name},
			{Text: "+" + amount(g.Kind, g.Gained), Tone: monitor.ToneGood},
			{Text: amount(g.Kind, g.Total), Tone: monitor.ToneMuted},
			{Text: g.Rate, Tone: monitor.ToneMuted},
			{Text: since, Tone: monitor.ToneMuted},
		})
	}
	return b
}

// summary is a chip per gain; with none, one saying so, unless there are
// drops to show instead.
func summary(rows []gain, drops bool) []monitor.Stat {
	if len(rows) == 0 && drops {
		return nil
	}
	if len(rows) == 0 {
		return []monitor.Stat{{Label: "Today", Value: "Nothing yet", Tone: monitor.ToneMuted}}
	}
	var out []monitor.Stat
	for _, g := range rows {
		out = append(out, monitor.Stat{Label: g.Name, Value: "+" + amount(g.Kind, g.Gained), Tone: monitor.ToneGood})
	}
	return out
}

// amount shows a number of kills plainly and XP in short: "85.2k xp".
func amount(k kind, n int64) string {
	if k != xp {
		return comma(n)
	}
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 2, 64) + "M xp"
	case n >= 10_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k xp"
	}
	return comma(n) + " xp"
}

func comma(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// snapshot is a WOM snapshot's numbers.
type snapshot struct {
	Data struct {
		Skills map[string]struct {
			Experience int64 `json:"experience"`
		} `json:"skills"`
		Bosses map[string]struct {
			Kills int64 `json:"kills"`
		} `json:"bosses"`
		Activities map[string]struct {
			Score int64 `json:"score"`
		} `json:"activities"`
	} `json:"data"`
}

func (s snapshot) stats() stats {
	out := stats{}
	set := func(id string, n int64) { out[id] = max(n, 0) }
	for id, v := range s.Data.Skills {
		set(id, v.Experience)
	}
	for id, v := range s.Data.Bosses {
		set(id, v.Kills)
	}
	for id, v := range s.Data.Activities {
		set(id, v.Score)
	}
	return out
}

// update asks WOM to read the player's hiscores again and returns the
// result. Asked again within a minute or so, WOM refuses (429), and the
// latest snapshot it has does.
func (m *Monitor) update(ctx context.Context, client *http.Client) (stats, error) {
	path := "/players/" + url.PathEscape(m.cfg.Player)
	var player struct {
		Latest *snapshot `json:"latestSnapshot"`
	}
	status, err := m.call(ctx, client, http.MethodPost, path, &player)
	if status == http.StatusTooManyRequests {
		status, err = m.call(ctx, client, http.MethodGet, path, &player)
	}
	if err != nil {
		return nil, err
	}
	if player.Latest == nil {
		return nil, fmt.Errorf("Wise Old Man has no stats for %s", m.cfg.Player)
	}
	return player.Latest.stats(), nil
}

// before is the player's last snapshot before t (within 30 days), or nil.
func (m *Monitor) before(ctx context.Context, client *http.Client, t time.Time) (stats, error) {
	q := url.Values{
		"startDate": {t.AddDate(0, 0, -30).UTC().Format(time.RFC3339)},
		"endDate":   {t.UTC().Format(time.RFC3339)},
		"limit":     {"1"},
	}
	var snaps []snapshot // newest first
	if _, err := m.call(ctx, client, http.MethodGet, "/players/"+url.PathEscape(m.cfg.Player)+"/snapshots?"+q.Encode(), &snaps); err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, nil
	}
	return snaps[0].stats(), nil
}

// call makes a request to WOM and decodes its reply into v, returning the
// status too.
func (m *Monitor) call(ctx context.Context, client *http.Client, method, path string, v any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.cfg.WOMURL+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("Wise Old Man %s %s: %s", method, path, resp.Status)
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}
