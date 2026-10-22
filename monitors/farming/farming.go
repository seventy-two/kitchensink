// Package farming times an Old School RuneScape player's farm runs, the
// ones that count towards the Tangleroot pet: when each patch will be
// ready, as RuneLite's Time Tracking plugin predicts it, and what's
// cheapest to plant where every choice gives the same chance.
//
// The game doesn't publish patches, so the data comes from RuneLite on the
// player's PC: the plugin saves what it sees in RuneLite's profile file,
// and the Windows client uploads the plugin's lines (see monitor.Upload).
// Predictions use RuneLite's own tables (runelite.go, generated) and
// arithmetic (patches.go).
package farming

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("farming", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const (
	// defaultFile is where RuneLite keeps per-account plugin data.
	defaultFile = `%USERPROFILE%\.runelite\profiles2\$rsprofile--1.properties`
	prefix      = "timetracking.rsprofile."
	userAgent   = "kitchensink (github.com/seventy-two/kitchensink)"
)

// Config is the monitor's config.
type Config struct {
	// Player is the OSRS name, as Wise Old Man tracks it, for the Farming
	// level: it sets which crops can be planted and the pet chance. It's
	// optional.
	Player string `json:"player"`
	// Profile is RuneLite's key for the account (the "GYL5utM5" in
	// timetracking.rsprofile.GYL5utM5.*). By default it's whichever
	// account RuneLite saw a patch for last.
	Profile string `json:"profile"`
	// File is RuneLite's profile file on the client's machine.
	File string `json:"file"`
	// Protect counts the gardeners' payment to protect trees in what a
	// crop costs. It defaults to true.
	Protect *bool `json:"protect"`
	// Timezone is for the times shown. It defaults to Europe/London.
	Timezone string `json:"timezone"`
	// PricesURL and WOMURL replace the OSRS Wiki's prices API and Wise
	// Old Man's, for tests.
	PricesURL string `json:"pricesURL"`
	WOMURL    string `json:"womURL"`
}

// Monitor is the farming monitor.
type Monitor struct {
	cfg Config
	loc *time.Location

	prices   map[int]int // GE price by item ID
	pricesAt time.Time
	level    int // Farming level, 0 if unknown
	levelAt  time.Time
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.File == "" {
		c.File = defaultFile
	}
	if c.Protect == nil {
		c.Protect = new(bool)
		*c.Protect = true
	}
	if c.Timezone == "" {
		c.Timezone = "Europe/London"
	}
	if c.PricesURL == "" {
		c.PricesURL = "https://prices.runescape.wiki/api/v1/osrs"
	}
	if c.WOMURL == "" {
		c.WOMURL = "https://api.wiseoldman.net/v2"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	return &Monitor{cfg: c, loc: loc}, nil
}

// upload is the last upload: every account's Time Tracking data by
// RuneLite's profile key.
type upload struct {
	At       time.Time          `json:"at"`
	Profiles map[string]profile `json:"profiles"`
}

const uploadKey = "upload"

// Act takes an upload of RuneLite's profile file.
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != monitor.UploadAction {
		return fmt.Errorf("%w: %q", monitor.ErrBadAction, a.Type)
	}
	profiles := parseProperties(a.Text)
	if len(profiles) == 0 {
		return fmt.Errorf("%w: no Time Tracking data", monitor.ErrBadAction)
	}
	return env.State.Set(uploadKey, upload{At: env.Now(), Profiles: profiles})
}

// parseProperties reads the plugin's lines from a Java properties file,
// e.g. `timetracking.rsprofile.GYL5utM5.11058.4771=226\:1788887118`.
func parseProperties(text string) map[string]profile {
	out := map[string]profile{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		rest, found := strings.CutPrefix(key, prefix)
		if !ok || !found {
			continue
		}
		id, k, ok := strings.Cut(rest, ".")
		if !ok {
			continue
		}
		if out[id] == nil {
			out[id] = profile{}
		}
		out[id][k] = strings.NewReplacer(`\:`, ":", `\=`, "=", `\\`, `\`).Replace(strings.TrimSpace(value))
	}
	return out
}

// pick chooses the configured account, or the one RuneLite saw a patch
// for last.
func (m *Monitor) pick(profiles map[string]profile) profile {
	if m.cfg.Profile != "" {
		return profiles[m.cfg.Profile]
	}
	var best profile
	var bestAt int64
	for _, p := range profiles {
		for k, v := range p {
			if strings.Count(k, ".") != 1 {
				continue // not a patch, e.g. "14651.4773.protected"
			}
			if _, at, ok := strings.Cut(v, ":"); ok {
				if n, _ := strconv.ParseInt(at, 10, 64); n > bestAt {
					best, bestAt = p, n
				}
			}
		}
	}
	return best
}

// Poll predicts every patch from the last upload.
func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	res := monitor.Result{Upload: &monitor.Upload{Path: m.cfg.File, Prefixes: []string{prefix}}}
	var up upload
	if _, err := env.State.Get(uploadKey, &up); err != nil {
		return res, err
	}
	pr := m.pick(up.Profiles)
	if pr == nil {
		res.Blocks = []monitor.Block{{Type: monitor.BlockText,
			Text: "Waiting for RuneLite's farming data from the Windows client. RuneLite's Time Tracking plugin needs to be on."}}
		return res, nil
	}
	m.refresh(ctx, env)

	now := env.Now()
	board := m.board(pr, now)
	res.Subtitle = m.subtitle(board)
	res.Blocks = m.blocks(board, now)
	res.Summary = m.summary(board, now)
	res.SummaryStyle = monitor.SummaryChips
	res.Events = m.events(board, now)
	if next := board.next(now); !next.IsZero() {
		res.Next = next.Sub(now) + 30*time.Second
	}
	return res, nil
}

// refresh fetches prices and the Farming level when they're old; a failure
// leaves the last ones (or none), as both only add detail.
func (m *Monitor) refresh(ctx context.Context, env monitor.Env) {
	now := env.Now()
	if now.Sub(m.pricesAt) >= 6*time.Hour {
		prices, err := m.fetchPrices(ctx, env.HTTP)
		if err != nil {
			env.Log.Warn("fetching prices", "err", err)
		} else {
			m.prices = prices
		}
		m.pricesAt = now
	}
	if m.cfg.Player != "" && now.Sub(m.levelAt) >= 6*time.Hour {
		level, err := m.fetchLevel(ctx, env.HTTP)
		if err != nil {
			env.Log.Warn("fetching Farming level", "err", err)
		} else {
			m.level = level
		}
		m.levelAt = now
	}
}

// row is one patch on the board.
type row struct {
	run    *run
	region region
	patch  patch
	pred   prediction
}

// board is the patches seen, by run, in the runs' order.
type board struct {
	rows  [][]row // by run
	level int
	seen  time.Time // when RuneLite last saw any patch
}

func (m *Monitor) board(pr profile, now time.Time) board {
	b := board{rows: make([][]row, len(runs)), level: m.level}
	for _, r := range regions {
		for _, p := range r.Patches {
			i := runOf(p.Impl)
			if i < 0 {
				continue
			}
			pred, ok := pr.predict(r, p, now)
			if !ok {
				continue
			}
			b.rows[i] = append(b.rows[i], row{run: &runs[i], region: r, patch: p, pred: pred})
			if pred.Seen.After(b.seen) {
				b.seen = pred.Seen
			}
		}
	}
	for _, rows := range b.rows {
		slices.SortStableFunc(rows, func(a, b row) int {
			return cmp.Or(cmp.Compare(slices.Index(a.run.Impls, a.patch.Impl), slices.Index(b.run.Impls, b.patch.Impl)),
				cmp.Compare(a.place(), b.place()))
		})
	}
	return b
}

// next is when the next patch finishes growing, or zero.
func (b board) next(now time.Time) time.Time {
	var next time.Time
	for _, rows := range b.rows {
		for _, r := range rows {
			if r.growing(now) && (next.IsZero() || r.pred.Done.Before(next)) {
				next = r.pred.Done
			}
		}
	}
	return next
}

// place names the patch, e.g. "Fossil Island east".
func (r row) place() string {
	name := r.region.Name
	if alias, ok := places[name]; ok {
		name = alias
	}
	switch r.patch.Name {
	case "", "Mushroom", "Hespori", "Anima":
		return name
	}
	return name + " " + strings.ToLower(r.patch.Name)
}

// places renames RuneLite's regions where its name isn't the one players
// use.
var places = map[string]string{
	"Seaweed":        "Fossil Island seaweed",
	"Morytania":      "Canifis",
	"Avium Savannah": "Locus Oasis",
}

// growing reports whether the patch is still growing at now.
func (r row) growing(now time.Time) bool {
	return r.pred.State == growing && r.pred.Produce != "WEEDS" && !r.pred.Done.IsZero() && now.Before(r.pred.Done)
}

// status says what the patch needs: nothing yet (with when it'll be
// ready), or a visit, and how it's shown.
func (r row) status(now time.Time, loc *time.Location) (text string, tone monitor.Tone, ready bool) {
	health := implementations[r.patch.Impl].HealthCheck
	switch {
	case r.pred.Produce == "WEEDS" || r.pred.State == empty:
		return "Plant", monitor.ToneAccent, true
	case r.growing(now):
		return when(r.pred.Done, now, loc), monitor.ToneNone, false
	case r.pred.State == growing && r.patch.Impl == "HESPORI":
		return "Fight", monitor.ToneGood, true
	case r.pred.State == growing && r.patch.Impl == "ANIMA":
		// An anima plant's growth ends in it dying.
		return "Replant", monitor.ToneAccent, true
	case r.pred.State == growing && health:
		return "Check health", monitor.ToneGood, true
	case r.pred.State == growing:
		return "Harvest", monitor.ToneGood, true
	case r.pred.State == harvestable && health:
		// Checked already: the next roll is a new tree.
		return "Replant", monitor.ToneAccent, true
	case r.pred.State == harvestable:
		return "Harvest", monitor.ToneGood, true
	case r.pred.State == diseased:
		return "Diseased", monitor.ToneWarn, true
	case r.pred.State == dead:
		return "Dead", monitor.ToneBad, true
	}
	return "", monitor.ToneMuted, false
}

// when is a time to come: the clock today, else the day too.
func when(t, now time.Time, loc *time.Location) string {
	t, now = t.In(loc), now.In(loc)
	if t.YearDay() == now.YearDay() && t.Year() == now.Year() {
		return t.Format("15:04")
	}
	if t.Sub(now) < 6*24*time.Hour {
		return t.Format("Mon 15:04")
	}
	return t.Format("2 Jan 15:04")
}

func (m *Monitor) subtitle(b board) string {
	var parts []string
	if b.level > 0 {
		parts = append(parts, fmt.Sprintf("Farming %d", b.level))
	}
	if !b.seen.IsZero() {
		parts = append(parts, "last seen in game "+b.seen.In(m.loc).Format("2 Jan 15:04"))
	}
	return strings.Join(parts, " · ")
}
