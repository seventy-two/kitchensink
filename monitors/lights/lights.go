// Package lights shows and switches smart lights through Home Assistant's
// REST API: one checklist row per light, ticked while it's off, with
// buttons to make it brighter or dimmer, and a row for all of them.
//
// Light changes make no events (they'd only be noise). If Home Assistant
// can't be reached for OfflineAfter, there's one quiet event, updated when
// it's back.
package lights

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("lights", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// URL is Home Assistant's base URL, e.g. "http://homeassistant.local:8123".
	URL string `json:"url"`
	// Token is a Home Assistant long-lived access token; use
	// "${HOMEASSISTANT_TOKEN}" to keep it out of the file.
	Token string `json:"token"`
	// Lights are entity IDs to show, in this order. With neither Lights
	// nor Areas, every light is shown, by name.
	Lights []string `json:"lights"`
	// Areas are Home Assistant areas (names or IDs) whose lights are
	// shown too, after Lights, by name.
	Areas []string `json:"areas"`
	// Levels are the brightness percentages the brighter and dimmer
	// buttons step between. They default to 10, 25, 50, 75 and 100.
	Levels []int `json:"levels"`
	// OfflineAfter is how long Home Assistant can be unreachable before a
	// quiet event says so. It defaults to 30 minutes; "0s" turns it off.
	OfflineAfter monitor.Duration `json:"offlineAfter"`
}

// Monitor is the lights panel.
type Monitor struct {
	cfg     Config
	include map[string]bool
	// recheck asks the poll after an action to bring the next one
	// forward, for lights that report their new state late.
	recheck bool
}

// recheckAfter is how soon to poll again after an action; the engine
// won't poll sooner than 10 seconds anyway.
const recheckAfter = 10 * time.Second

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{OfflineAfter: monitor.Duration(30 * time.Minute)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	u, err := url.Parse(c.URL)
	if c.URL == "" || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("url must be Home Assistant's address, like http://homeassistant.local:8123")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Token == "" {
		return nil, fmt.Errorf("token is required (a long-lived access token, from an environment variable)")
	}
	if len(c.Levels) == 0 {
		c.Levels = []int{10, 25, 50, 75, 100}
	}
	for _, l := range c.Levels {
		if l < 1 || l > 100 {
			return nil, fmt.Errorf("levels must be percentages from 1 to 100, not %d", l)
		}
	}
	c.Levels = slices.Clone(c.Levels)
	slices.Sort(c.Levels)
	c.Levels = slices.Compact(c.Levels)
	if c.OfflineAfter < 0 {
		return nil, fmt.Errorf("offlineAfter can't be negative")
	}
	m := &Monitor{cfg: c, include: map[string]bool{}}
	for _, id := range c.Lights {
		if !strings.HasPrefix(id, "light.") {
			return nil, fmt.Errorf("lights are entity IDs like light.desk, not %q", id)
		}
		m.include[id] = true
	}
	return m, nil
}

// light is one light as the panel shows it.
type light struct {
	ID        string
	Name      string
	On        bool
	Available bool
	Dimmable  bool
	Pct       int // brightness while on, 1-100
}

// allID is the checklist row for every light at once.
const allID = "all"

func (m *Monitor) client(env monitor.Env) *client {
	return &client{http: env.HTTP, base: m.cfg.URL, token: m.cfg.Token}
}

// lights fetches the lights to show, in order.
func (m *Monitor) lights(ctx context.Context, env monitor.Env) ([]light, error) {
	c := m.client(env)
	states, err := c.states(ctx)
	if err != nil {
		return nil, err
	}
	var inArea map[string]bool
	if len(m.cfg.Areas) > 0 {
		if inArea, err = c.areaLights(ctx, m.cfg.Areas); err != nil {
			return nil, err
		}
	}
	all := len(m.cfg.Lights) == 0 && len(m.cfg.Areas) == 0

	byID := map[string]light{}
	var named []light // shown by name, after Lights
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "light.") {
			continue
		}
		l := toLight(s)
		switch {
		case m.include[s.EntityID]:
			byID[s.EntityID] = l
		case all || inArea[s.EntityID]:
			named = append(named, l)
		}
	}
	slices.SortFunc(named, func(a, b light) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	out := make([]light, 0, len(m.cfg.Lights)+len(named))
	for _, id := range m.cfg.Lights {
		l, ok := byID[id]
		if !ok {
			// Gone from Home Assistant, or renamed: say so rather than
			// quietly dropping it.
			l = light{ID: id, Name: id}
		}
		out = append(out, l)
	}
	return append(out, named...), nil
}

func toLight(s state) light {
	l := light{
		ID:        s.EntityID,
		Name:      s.Attributes.FriendlyName,
		On:        s.State == "on",
		Available: s.State == "on" || s.State == "off",
	}
	if l.Name == "" {
		l.Name = s.EntityID
	}
	for _, mode := range s.Attributes.SupportedColorModes {
		if mode != "onoff" && mode != "unknown" {
			l.Dimmable = true
		}
	}
	if l.On && s.Attributes.Brightness != nil {
		l.Dimmable = true
		l.Pct = max(1, min(100, int(math.Round(*s.Attributes.Brightness*100/255))))
	}
	return l
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	lights, err := m.lights(ctx, env)
	if err != nil {
		return monitor.Result{Events: m.offline(env, err)}, err
	}
	res := build(lights)
	res.Events = m.online(env)
	if m.recheck {
		m.recheck = false
		res.Next = recheckAfter
	}
	return res, nil
}

// build makes the panel and summary.
func build(lights []light) monitor.Result {
	if len(lights) == 0 {
		return monitor.Result{
			Blocks:  []monitor.Block{{Type: monitor.BlockText, Text: "No lights found in Home Assistant."}},
			Summary: []monitor.Stat{{Label: "Lights", Value: "None", Tone: monitor.ToneMuted}},
		}
	}
	on := 0
	for _, l := range lights {
		if l.On {
			on++
		}
	}

	// A ticked row is a light that's off: ticking a row turns it off,
	// unticking turns it on, and the brighter and dimmer buttons are only
	// offered on lights that are on (clients leave them off ticked rows).
	b := monitor.Block{
		Type:     monitor.BlockChecklist,
		NoDelete: true,
		Movable:  true,
		UpHint:   "Brighter",
		DownHint: "Dimmer",
	}
	if len(lights) > 1 {
		title := "All lights"
		if on > 0 {
			title += fmt.Sprintf(" · %d of %d on", on, len(lights))
		}
		b.Items = append(b.Items, monitor.Item{ID: allID, Title: title, Done: on == 0})
	}
	summary := []monitor.Stat{{Label: "Lights", Value: "All off", Tone: monitor.ToneMuted, Lead: true}}
	if on > 0 {
		summary[0].Value = fmt.Sprintf("%d of %d on", on, len(lights))
		summary[0].Tone = monitor.ToneAccent
	}
	for _, l := range lights {
		it := monitor.Item{ID: l.ID, Title: l.Name, Done: !l.On}
		switch {
		case !l.Available:
			it.Title += " · unavailable"
			it.Tone = monitor.ToneWarn
			summary = append(summary, monitor.Stat{Label: l.Name, Value: "Unavailable", Tone: monitor.ToneWarn})
		case l.On:
			it.Title += " · " + level(l)
			summary = append(summary, monitor.Stat{Label: l.Name, Value: level(l), Tone: monitor.ToneAccent})
		}
		b.Items = append(b.Items, it)
	}
	return monitor.Result{Blocks: []monitor.Block{b}, Summary: summary, SummaryStyle: monitor.SummaryChips}
}

// level is how bright a light that's on is.
func level(l light) string {
	if !l.Dimmable || l.Pct == 0 {
		return "on"
	}
	return strconv.Itoa(l.Pct) + "%"
}

// Act takes a client's change:
//   - "toggle" switches a light (ID its entity ID) off if it's on and on
//     if it's off; for "all", every light off if any is on, else every
//     light on.
//   - "move" with Text "up" or "down" makes a light (or every light
//     that's on, for "all") brighter or dimmer, to the next of Levels.
//   - "brightness" with Text a percentage sets a light's brightness, 0
//     turning it off.
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	lights, err := m.lights(ctx, env)
	if err != nil {
		return err
	}
	targets := lights
	if a.ID != allID {
		i := slices.IndexFunc(lights, func(l light) bool { return l.ID == a.ID })
		if i < 0 {
			return fmt.Errorf("%w: no light %q", monitor.ErrBadAction, a.ID)
		}
		if !lights[i].Available {
			return fmt.Errorf("%w: %s is unavailable", monitor.ErrBadAction, lights[i].Name)
		}
		targets = lights[i : i+1]
	}
	c := m.client(env)

	switch a.Type {
	case "toggle":
		var on, available []string
		for _, l := range targets {
			if l.On {
				on = append(on, l.ID)
			}
			if l.Available {
				available = append(available, l.ID)
			}
		}
		switch {
		case len(on) > 0:
			err = c.call(ctx, "turn_off", map[string]any{"entity_id": on})
		case len(available) > 0:
			err = c.call(ctx, "turn_on", map[string]any{"entity_id": available})
		default:
			return fmt.Errorf("%w: no lights are available", monitor.ErrBadAction)
		}
	case "move":
		up := a.Text == "up"
		if !up && a.Text != "down" {
			return fmt.Errorf("%w: move up or down, not %q", monitor.ErrBadAction, a.Text)
		}
		// Each light steps from its own level; one call per level reached.
		to := map[int][]string{}
		for _, l := range targets {
			if !l.On || !l.Dimmable {
				continue
			}
			if p, ok := step(l.Pct, m.cfg.Levels, up); ok {
				to[p] = append(to[p], l.ID)
			}
		}
		// Already as bright or dim as the levels go: nothing to do.
		for _, p := range slices.Sorted(maps.Keys(to)) {
			if err = c.call(ctx, "turn_on", map[string]any{"entity_id": to[p], "brightness_pct": p}); err != nil {
				break
			}
		}
	case "brightness":
		p, perr := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(a.Text), "%"))
		if perr != nil || p < 0 || p > 100 {
			return fmt.Errorf("%w: brightness is a percentage from 0 to 100, not %q", monitor.ErrBadAction, a.Text)
		}
		var ids []string
		for _, l := range targets {
			if l.Available && (p == 0 || l.Dimmable) {
				ids = append(ids, l.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if p == 0 {
			err = c.call(ctx, "turn_off", map[string]any{"entity_id": ids})
		} else {
			err = c.call(ctx, "turn_on", map[string]any{"entity_id": ids, "brightness_pct": p})
		}
	default:
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	if err != nil {
		return err
	}
	m.recheck = true
	return nil
}

// step returns the level after pct, brighter (up) or dimmer, and false at
// either end.
func step(pct int, levels []int, up bool) (int, bool) {
	if up {
		for _, l := range levels {
			if l > pct {
				return l, true
			}
		}
		return 0, false
	}
	for i := len(levels) - 1; i >= 0; i-- {
		if levels[i] < pct {
			return levels[i], true
		}
	}
	return 0, false
}

// outage is the state kept while Home Assistant can't be reached.
type outage struct {
	Since time.Time `json:"since"`
	Told  bool      `json:"told"` // the event has been raised
}

const outageKey = "outage"

func outageEvent(o outage) string { return "offline/" + o.Since.UTC().Format(time.RFC3339) }

// offline notes a failed poll, and returns the one quiet event once it's
// gone on for OfflineAfter.
func (m *Monitor) offline(env monitor.Env, cause error) []monitor.Event {
	var o outage
	if _, err := env.State.Get(outageKey, &o); err != nil {
		env.Log.Error("reading outage", "err", err)
		return nil
	}
	now := env.Now()
	if o.Since.IsZero() {
		o.Since = now
	}
	var events []monitor.Event
	after := time.Duration(m.cfg.OfflineAfter)
	if !o.Told && after > 0 && now.Sub(o.Since) >= after {
		o.Told = true
		title := "Can't reach Home Assistant"
		if !errors.Is(cause, errUnreachable) {
			title = "Home Assistant isn't answering properly"
		}
		events = append(events, monitor.Event{
			Key:      outageEvent(o),
			Title:    title,
			Body:     fmt.Sprintf("Since %s: %v", o.Since.Format("15:04 Mon 2 Jan"), cause),
			Priority: monitor.PriorityLow,
			Quiet:    true,
		})
	}
	if err := env.State.Set(outageKey, o); err != nil {
		env.Log.Error("saving outage", "err", err)
	}
	return events
}

// online ends any outage, updating its event if there was one.
func (m *Monitor) online(env monitor.Env) []monitor.Event {
	var o outage
	ok, err := env.State.Get(outageKey, &o)
	if err != nil || !ok || o.Since.IsZero() {
		return nil
	}
	if err := env.State.Set(outageKey, outage{}); err != nil {
		env.Log.Error("saving outage", "err", err)
	}
	if !o.Told {
		return nil
	}
	now := env.Now()
	return []monitor.Event{{
		Key:      outageEvent(o),
		Update:   true,
		Title:    "Home Assistant was unreachable for " + span(now.Sub(o.Since)),
		Quiet:    true,
		Until:    now.Add(time.Hour),
		Priority: monitor.PriorityLow,
	}}
}

// span writes a duration as hours and minutes, e.g. "2h 5m" or "40m".
func span(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return strconv.Itoa(m) + "m"
	}
	if m%60 == 0 {
		return strconv.Itoa(m/60) + "h"
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}
