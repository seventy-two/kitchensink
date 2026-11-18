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
	"github.com/seventy-two/kitchensink/monitors/farming"
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
	// Owned are the pets the player already has, by name ("Giant
	// squirrel", "Kraken"): their skills, bosses and activities aren't
	// shown. A pet Dink reports that isn't a duplicate is added by itself.
	Owned []string `json:"owned"`
	// WOMURL replaces Wise Old Man's API, for tests.
	WOMURL string `json:"womURL"`
	// HistoryLoot is the least a drop must be worth to be kept for good in
	// the drop history (with pets), beyond today. It defaults to 10m.
	HistoryLoot int64 `json:"historyLoot"`
	// Publish, if set, is a file the monitor writes after each poll with
	// the player's counts and the drop history, for a public page (such as
	// petsheet's progress charts) to read. See publish.go.
	Publish string `json:"publish"`
	// ProfileFile is RuneLite's profile file (e.g.
	// "%USERPROFILE%\.runelite\profiles2\main-1.properties"), where
	// plugins keep counts the hiscores don't: the Delve Calculator's Doom
	// of Mokhaiotl delve levels (Dom's chance depends on the level) and the
	// goat hunting plugin's goats caught (Mr McGroot). The Windows client
	// uploads their lines when they change (looking every minute), and the
	// counts are published with the rest; goats also count in the day's
	// gains. See plugins.go.
	ProfileFile string `json:"profileFile"`
	// PublishUniques, if set, is a file the monitor writes after each poll
	// with every collection log item from the tracked bosses (see
	// uniques.go), for petsheet's est. profit.
	PublishUniques string `json:"publishUniques"`
	// Commons are items, by metric, kept with the uniques though they
	// aren't in the collection log, where they're most of a boss's money
	// (Superior dragon bones at Vorkath: {"vorkath": [22124]}).
	Commons map[string][]int `json:"commons"`
	// LogItemsURL replaces TempleOSRS's list of collection log items, for
	// tests.
	LogItemsURL string `json:"logItemsURL"`
}

// womEvery is how often Wise Old Man is asked to update the player, at
// most. The hiscores only change when the player logs out, and Dink's
// hooks (which have the monitor poll after every kill) cover the time in
// between, so WOM is only a backstop (the user's call: every 6 hours).
const womEvery = 6 * time.Hour

// womRetry is how long to wait after WOM fails before asking again. Its
// API is often down or slow; a failure is only logged, and the last stats
// (or Dink's alone) are shown meanwhile.
const womRetry = 30 * time.Minute

// womKey keeps the last WOM stats across restarts, so a restart doesn't
// need WOM.
const womKey = "wom"

// womState is what womKey holds.
type womState struct {
	At    time.Time `json:"at"`
	Stats stats     `json:"stats"`
}

// Monitor is the OSRS monitor.
type Monitor struct {
	cfg Config
	loc *time.Location

	// The player's stats from WOM, and when; loaded tells whether
	// womKey has been read since starting.
	wom    stats
	womAt  time.Time
	loaded bool
	// When WOM last failed, for the update and for the day's start.
	womFailed, baseFailed time.Time
	// When a chat message last had the panel refresh, and whether one
	// since hasn't (see chatEvery).
	chatAt  time.Time
	pending bool
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
	if c.HistoryLoot == 0 {
		c.HistoryLoot = 10_000_000
	}
	if c.LogItemsURL == "" {
		c.LogItemsURL = defaultLogItems
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
// day's start; without one, the last earlier snapshot does. Where Dink
// counted more before midnight, its counts mark the start instead.
func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now().In(m.loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.loc)
	day := midnight.Format(time.DateOnly)

	if err := m.refreshWOM(ctx, env); err != nil {
		return monitor.Result{}, err
	}
	current := m.wom
	if current == nil {
		current = stats{} // WOM hasn't answered yet: Dink's counts alone
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
	var base baseline
	if _, err := env.State.Get(baselineKey, &base); err != nil {
		return monitor.Result{}, err
	}
	if base.Day != day {
		start, keep := m.dayStart(ctx, env, midnight, current)
		// The hiscores only update at logout, so mid-session the snapshot
		// lags. Until Dink speaks today, all it has counted is from before
		// midnight (and once it has, today's Starts hold that instead).
		if td.Day != day {
			start = merged(start, live)
		}
		base = baseline{Day: day, Stats: start}
		if keep {
			if err := env.State.Set(baselineKey, base); err != nil {
				return monitor.Result{}, err
			}
		}
	}

	from := base.Stats
	if td.Day == day && len(td.Starts) > 0 {
		from = merged(base.Stats, td.Starts)
	}
	owned, err := m.owned(env)
	if err != nil {
		return monitor.Result{}, err
	}
	var rolls []farming.Roll
	if _, err := env.State.Get(farmingKey, &rolls); err != nil {
		return monitor.Result{}, err
	}
	var extra []gain
	if g, ok := farmingGain(rolls, midnight); ok {
		extra = append(extra, g)
	}
	hunting, err := m.hunterGains(env, day, merged(current, live)["hunter"])
	if err != nil {
		return monitor.Result{}, err
	}
	extra = append(extra, hunting...)
	rows := gains(from, merged(current, live), owned, extra...)
	if err := m.publish(env, merged(current, live)); err != nil {
		return monitor.Result{}, err
	}
	if m.cfg.PublishUniques != "" {
		m.refreshLogItems(ctx, env)
		if err := m.publishUniques(env); err != nil {
			return monitor.Result{}, err
		}
	}
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
	var seen time.Time
	if _, err := env.State.Get(seenKey, &seen); err != nil {
		return monitor.Result{}, err
	}
	var relevant monitor.Window
	if !seen.IsZero() {
		relevant.Until = seen.Add(playing)
	}
	var upload *monitor.Upload
	if m.cfg.ProfileFile != "" {
		// Once a day is enough for pets this rare (the user's call).
		// Every minute, for the day's goats (only changed lines are sent).
		upload = &monitor.Upload{Path: m.cfg.ProfileFile, Prefixes: []string{delvePrefix, goatPrefix}, Every: 60}
	}
	next := midnight.AddDate(0, 0, 1).Add(time.Minute).Sub(now)
	if m.pending {
		m.pending = false
		next = min(next, chatEvery)
	}
	return monitor.Result{
		Upload:       upload,
		Relevant:     relevant,
		Blocks:       blocks,
		Summary:      append(dropChips(drops), summary(rows, len(drops) > 0)...),
		SummaryStyle: monitor.SummaryChips,
		SummaryBlock: &short,
		Events:       events,
		Next:         next,
	}, nil
}

// refreshWOM loads the last WOM stats from state after a restart, then
// asks WOM again when they're womEvery old. A failure is only logged
// (WOM is unreliable and Dink covers the gap) and retried after
// womRetry; only a state error is returned.
func (m *Monitor) refreshWOM(ctx context.Context, env monitor.Env) error {
	if !m.loaded {
		var w womState
		if _, err := env.State.Get(womKey, &w); err != nil {
			return err
		}
		m.wom, m.womAt, m.loaded = w.Stats, w.At, true
	}
	now := env.Now()
	if m.wom != nil && now.Sub(m.womAt) < womEvery {
		return nil
	}
	if now.Sub(m.womFailed) < womRetry {
		return nil
	}
	wom, err := m.update(ctx, env.HTTP)
	if err != nil {
		m.womFailed = now
		if env.Log != nil {
			env.Log.Warn("osrs: Wise Old Man update failed; using the last stats", "err", err)
		}
		return nil
	}
	m.wom, m.womAt = wom, now
	return env.State.Set(womKey, womState{At: now, Stats: wom})
}

// dayStart is the player's stats at midnight, from WOM's last snapshot
// before it, and whether it's sure enough to keep for the day. When WOM
// fails, stats fetched before midnight will do (and are kept); failing
// that, current stands in until WOM answers (tried again after womRetry).
func (m *Monitor) dayStart(ctx context.Context, env monitor.Env, midnight time.Time, current stats) (stats, bool) {
	if env.Now().Sub(m.baseFailed) >= womRetry {
		start, err := m.before(ctx, env.HTTP, midnight.Add(5*time.Minute))
		if err == nil {
			if start == nil {
				return current, true // nothing earlier: today starts now
			}
			return start, true
		}
		m.baseFailed = env.Now()
		if env.Log != nil {
			env.Log.Warn("osrs: Wise Old Man snapshots failed", "err", err)
		}
	}
	if m.wom != nil && m.womAt.Before(midnight) {
		return m.wom, true
	}
	return current, false
}

// byRolls are the skills counted by their pets' rolls, not XP.
var byRolls = map[string]bool{"farming": true, "hunter": true}

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
	// Today, if set, is how the gain is shown, in place of Gained, and
	// there's no total (Farming's patches checked).
	Today string
}

// gains are the tracked metrics that went up, leaving out those whose pet
// is owned (by petKey): kills and scores first, then XP, each most first.
// Farming and Hunter come from the rolls for their pets instead of XP
// (see farmingGain and hunterGains).
func gains(from, to stats, owned map[string]bool, extra ...gain) []gain {
	var out []gain
	for id, mt := range tracked {
		if owned[petKey(mt.Pet)] || byRolls[id] {
			continue
		}
		if d := to[id] - from[id]; d > 0 {
			out = append(out, gain{metric: mt, ID: id, Gained: d, Total: to[id]})
		}
	}
	for _, g := range extra {
		if !owned[petKey(g.Pet)] {
			out = append(out, g)
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
		today, total := "+"+amount(g.Kind, g.Gained), amount(g.Kind, g.Total)
		if g.Today != "" {
			today, total = g.Today, ""
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			{Text: g.Name},
			{Text: today, Tone: monitor.ToneGood},
			{Text: total, Tone: monitor.ToneMuted},
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
		v := "+" + amount(g.Kind, g.Gained)
		if g.Today != "" {
			v = g.Today
		}
		out = append(out, monitor.Stat{Label: g.Name, Value: v, Tone: monitor.ToneGood})
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
