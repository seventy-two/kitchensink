// Package geprices watches Grand Exchange prices of a few OSRS items, from
// the OSRS Wiki's real-time prices API, and notifies when one reaches its
// target. Items are added and removed from clients: "ascension crossbows
// 150m" watches that item with an alert at 150m.
//
// The API's rules (oldschool.runescape.wiki/w/RuneScape:Real-time_Prices)
// ask for a User-Agent saying what's asking, and for no more requests than
// needed: prices are fetched at most once per MinFetch however often the
// monitor is polled (adding an item polls at once), one request per item
// while there are few and the whole /latest list once there are more, as
// the rules prefer that to looping over ids. The item list (/mapping) is
// only fetched to look up a name being added, and kept for a day.
package geprices

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("geprices", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// userAgent is what the API asks for: what this is and where it lives.
const userAgent = "kitchensink-geprices/1.0 (personal dashboard price alerts; github.com/seventy-two/kitchensink)"

// Config is the monitor's config.
type Config struct {
	// URL replaces the API's base, for tests.
	URL string `json:"url"`
	// MinFetch is the shortest time between price fetches; 4m30s by
	// default, so a 5m interval fetches every poll and actions none extra.
	MinFetch monitor.Duration `json:"minFetch"`
}

// Monitor watches the items in its state.
type Monitor struct {
	url      string
	minFetch time.Duration

	mu      sync.Mutex
	fetched time.Time     // when prices were last fetched
	prices  map[int]price // the last fetch, by item id
	mapping []mapped      // the API's item list, for adding by name
	mapped  time.Time     // when mapping was fetched
	have    map[int]bool  // the ids prices holds an answer for
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{URL: "https://prices.runescape.wiki/api/v1/osrs", MinFetch: monitor.Duration(4*time.Minute + 30*time.Second)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	return &Monitor{url: strings.TrimRight(c.URL, "/"), minFetch: time.Duration(c.MinFetch)}, nil
}

// watch is one item being watched.
type watch struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	// Target is the alert price in coins; 0 for none.
	Target int64 `json:"target,omitempty"`
	// Above alerts when the price rises to Target, otherwise when it falls
	// to it.
	Above bool `json:"above,omitempty"`
	// Armed is when the alert was set: only trades after it count, so an
	// old one doesn't fire it at once.
	Armed time.Time `json:"armed"`
	// Hit is when it fired; the alert is off until it's set again.
	Hit *time.Time `json:"hit,omitempty"`
	// HitAt is the price it fired at.
	HitAt int64 `json:"hitAt,omitempty"`
}

// price is an item's latest instant-buy (High) and instant-sell (Low)
// prices, as /latest gives them. Times are Unix seconds; zero values are
// missing (nothing traded that way yet).
type price struct {
	High     int64 `json:"high"`
	HighTime int64 `json:"highTime"`
	Low      int64 `json:"low"`
	LowTime  int64 `json:"lowTime"`
}

type mapped struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

const itemsKey = "items"

// maxItems keeps the list to what's worth a panel.
const maxItems = 30

// perItem is the most items fetched one request each; with more, the whole
// list is fetched once.
const perItem = 5

func load(env monitor.Env) ([]watch, error) {
	var w []watch
	if _, err := env.State.Get(itemsKey, &w); err != nil {
		return nil, fmt.Errorf("reading items: %w", err)
	}
	return w, nil
}

// Act takes "add" (Text: an item name or id, then optionally a target such
// as "150m", "<150m" or ">2.5b"; adding one already watched changes its
// target), "delete" (ID: the item id) and "toggle" (ID: turns its alert
// back on, or off).
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	items, err := load(env)
	if err != nil {
		return err
	}
	find := func() (int, error) {
		for i, w := range items {
			if strconv.Itoa(w.ID) == a.ID {
				return i, nil
			}
		}
		return -1, fmt.Errorf("%w: not watching item %q", monitor.ErrBadAction, a.ID)
	}
	now := env.Now()
	switch a.Type {
	case "add":
		query, target, dir, err := parseAdd(a.Text)
		if err != nil {
			return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
		}
		item, err := m.lookup(ctx, env, query)
		if err != nil {
			return err
		}
		w := watch{ID: item.ID, Name: item.Name, Target: target, Armed: now}
		if target > 0 {
			w.Above = dir == '>'
			if dir == 0 {
				// No direction given: towards the target from where the
				// price is now.
				if p, err := m.priceNow(ctx, env, item.ID); err == nil && p > 0 {
					w.Above = target > p
				}
			}
		}
		replaced := false
		for i := range items {
			if items[i].ID == w.ID {
				items[i], replaced = w, true
			}
		}
		if !replaced {
			if len(items) >= maxItems {
				return fmt.Errorf("%w: watching %d items already", monitor.ErrBadAction, maxItems)
			}
			items = append(items, w)
		}
	case "delete":
		i, err := find()
		if err != nil {
			return err
		}
		items = append(items[:i], items[i+1:]...)
	case "toggle":
		i, err := find()
		if err != nil {
			return err
		}
		if items[i].Target == 0 {
			return fmt.Errorf("%w: %s has no target; add it again with one, e.g. %q", monitor.ErrBadAction, items[i].Name, strings.ToLower(items[i].Name)+" 150m")
		}
		if items[i].Hit != nil {
			items[i].Hit, items[i].HitAt, items[i].Armed = nil, 0, now
		} else {
			items[i].Hit, items[i].HitAt = &now, 0
		}
	default:
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	return env.State.Set(itemsKey, items)
}

// parseAdd splits "ascension crossbows <150m" into the item, the target in
// coins (0 for none) and its direction ('<', '>' or 0 for unsaid).
func parseAdd(text string) (query string, target int64, dir byte, err error) {
	f := strings.Fields(text)
	if len(f) == 0 {
		return "", 0, 0, fmt.Errorf("name an item, e.g. \"ascension crossbows 150m\"")
	}
	last := f[len(f)-1]
	switch {
	case strings.HasPrefix(last, "<") || strings.HasPrefix(last, ">"):
		dir, last = last[0], last[1:]
		if last == "" && len(f) > 1 {
			// "item < 150m"
			return "", 0, 0, fmt.Errorf("write the target without a space, e.g. \"<150m\"")
		}
	}
	if v, ok := parseCoins(last); ok && len(f) > 1 {
		return strings.Join(f[:len(f)-1], " "), v, dir, nil
	}
	if dir != 0 {
		return "", 0, 0, fmt.Errorf("%q isn't a price; try e.g. \"<150m\"", f[len(f)-1])
	}
	return strings.Join(f, " "), 0, 0, nil
}

// parseCoins reads "150m", "2.5b", "900k", "1,200,000" or "150000000".
func parseCoins(s string) (int64, bool) {
	s = strings.ToLower(strings.ReplaceAll(s, ",", ""))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult = 1e3
	case strings.HasSuffix(s, "m"):
		mult = 1e6
	case strings.HasSuffix(s, "b"):
		mult = 1e9
	}
	if mult != 1 {
		s = s[:len(s)-1]
	} else if len(s) < 4 {
		// A bare small number is more likely part of a name ("Dragon
		// dart 4") or an item id than a price.
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || v*mult > 1e12 {
		return 0, false
	}
	return int64(v*mult + 0.5), true
}

// lookup finds an item by id or name: an exact name, else the one name
// containing the query.
func (m *Monitor) lookup(ctx context.Context, env monitor.Env, query string) (mapped, error) {
	list, err := m.itemList(ctx, env)
	if err != nil {
		return mapped{}, err
	}
	if id, err := strconv.Atoi(query); err == nil {
		for _, it := range list {
			if it.ID == id {
				return it, nil
			}
		}
		return mapped{}, fmt.Errorf("%w: no item with id %d", monitor.ErrBadAction, id)
	}
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	var near []mapped
	for _, it := range list {
		name := strings.ToLower(it.Name)
		if name == q {
			return it, nil
		}
		if strings.Contains(name, q) {
			near = append(near, it)
		}
	}
	switch len(near) {
	case 0:
		return mapped{}, fmt.Errorf("%w: no GE item called %q", monitor.ErrBadAction, query)
	case 1:
		return near[0], nil
	}
	sort.Slice(near, func(i, j int) bool { return len(near[i].Name) < len(near[j].Name) })
	names := make([]string, 0, 5)
	for _, it := range near[:min(5, len(near))] {
		names = append(names, it.Name)
	}
	more := ""
	if len(near) > 5 {
		more = fmt.Sprintf(" and %d more", len(near)-5)
	}
	return mapped{}, fmt.Errorf("%w: %q could be %s%s", monitor.ErrBadAction, query, strings.Join(names, ", "), more)
}

func (m *Monitor) itemList(ctx context.Context, env monitor.Env) ([]mapped, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mapping != nil && env.Now().Sub(m.mapped) < 24*time.Hour {
		return m.mapping, nil
	}
	var list []mapped
	if err := m.get(ctx, env, "/mapping", &list); err != nil {
		return nil, fmt.Errorf("fetching the item list: %w", err)
	}
	m.mapping, m.mapped = list, env.Now()
	return list, nil
}

// priceNow is an item's latest instant-sell price (or instant-buy, if it
// has none), from the last fetch if it's there.
func (m *Monitor) priceNow(ctx context.Context, env monitor.Env, id int) (int64, error) {
	m.mu.Lock()
	p, ok := m.prices[id]
	m.mu.Unlock()
	if !ok {
		var body struct {
			Data map[string]price `json:"data"`
		}
		if err := m.get(ctx, env, "/latest?id="+strconv.Itoa(id), &body); err != nil {
			return 0, err
		}
		p = body.Data[strconv.Itoa(id)]
	}
	if p.Low > 0 {
		return p.Low, nil
	}
	return p.High, nil
}

// fetch gets the latest prices of the items, unless it did less than
// minFetch ago and has them all.
func (m *Monitor) fetch(ctx context.Context, env monitor.Env, items []watch) (map[int]price, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := env.Now()
	missing := false
	for _, w := range items {
		if !m.have[w.ID] {
			missing = true
		}
	}
	if !missing && now.Sub(m.fetched) < m.minFetch {
		return m.prices, nil
	}
	got := map[int]price{}
	if len(items) <= perItem {
		for _, w := range items {
			var body struct {
				Data map[string]price `json:"data"`
			}
			if err := m.get(ctx, env, "/latest?id="+strconv.Itoa(w.ID), &body); err != nil {
				return nil, err
			}
			if p, ok := body.Data[strconv.Itoa(w.ID)]; ok {
				got[w.ID] = p
			}
		}
	} else {
		var body struct {
			Data map[string]price `json:"data"`
		}
		if err := m.get(ctx, env, "/latest", &body); err != nil {
			return nil, err
		}
		for k, p := range body.Data {
			if id, err := strconv.Atoi(k); err == nil {
				got[id] = p
			}
		}
	}
	m.prices, m.fetched = got, now
	m.have = map[int]bool{}
	for _, w := range items {
		m.have[w.ID] = true // even if never traded: asked, and no answer
	}
	return got, nil
}

func (m *Monitor) get(ctx context.Context, env monitor.Env, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.url+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := env.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("prices API %s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(v)
}

// hit is the trade that reached w's target since it was armed, if any: for
// a fall, an instant-sell at or under it (a buy offer there filled); for a
// rise, an instant-buy at or over it.
func hit(w watch, p price) (int64, time.Time, bool) {
	if w.Target == 0 || w.Hit != nil {
		return 0, time.Time{}, false
	}
	armed := w.Armed.Unix()
	if w.Above {
		if p.High >= w.Target && p.HighTime >= armed {
			return p.High, time.Unix(p.HighTime, 0), true
		}
	} else if p.Low > 0 && p.Low <= w.Target && p.LowTime >= armed {
		return p.Low, time.Unix(p.LowTime, 0), true
	}
	return 0, time.Time{}, false
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	items, err := load(env)
	if err != nil {
		return monitor.Result{}, err
	}
	prices := map[int]price{}
	if len(items) > 0 {
		if prices, err = m.fetch(ctx, env, items); err != nil {
			return monitor.Result{}, err
		}
	}
	now := env.Now()
	var events []monitor.Event
	changed := false
	for i, w := range items {
		at, when, ok := hit(w, prices[w.ID])
		if !ok {
			continue
		}
		items[i].Hit, items[i].HitAt, changed = &when, at, true
		way := "fallen"
		if w.Above {
			way = "risen"
		}
		events = append(events, monitor.Event{
			Key:      fmt.Sprintf("hit/%d/%d", w.ID, w.Armed.Unix()),
			Time:     now,
			Title:    fmt.Sprintf("%s has %s to %s", w.Name, way, coins(at)),
			Body:     fmt.Sprintf("Traded at %s on the GE at %s (your target %s).", coins(at), when.In(london).Format("15:04"), coins(w.Target)),
			URL:      itemURL(w.ID),
			Priority: monitor.PriorityHigh,
		})
	}
	if changed {
		if err := env.State.Set(itemsKey, items); err != nil {
			return monitor.Result{}, err
		}
	}

	b := monitor.Block{
		Type:       monitor.BlockChecklist,
		Items:      []monitor.Item{},
		AddHint:    "Add an item, e.g. ascension crossbows 150m",
		DeleteHint: "Stop watching",
	}
	var summary []monitor.Stat
	for _, w := range items {
		p, traded := prices[w.ID]
		it := monitor.Item{ID: strconv.Itoa(w.ID), Title: line(w, p, traded), URL: itemURL(w.ID), Done: w.Target > 0 && w.Hit != nil}
		st := monitor.Stat{Label: w.Name, Value: "no trades", Tone: monitor.ToneMuted, URL: itemURL(w.ID)}
		if traded {
			st.Value, st.Tone = coins(mid(p)), monitor.ToneNone
		}
		if w.Target > 0 {
			st.Detail = target(w)
			if w.Hit != nil {
				st.Tone, it.Tone = monitor.ToneGood, monitor.ToneGood
			}
		}
		b.Items = append(b.Items, it)
		summary = append(summary, st)
	}
	if len(items) == 0 {
		summary = []monitor.Stat{{Label: "GE", Value: "Nothing watched", Tone: monitor.ToneMuted}}
	}
	return monitor.Result{
		Blocks:       []monitor.Block{b},
		Summary:      summary,
		SummaryStyle: monitor.SummaryChips,
		Timely:       len(items) > 0,
		Events:       events,
	}, nil
}

var london = func() *time.Location {
	l, err := time.LoadLocation("Europe/London")
	if err != nil {
		return time.UTC
	}
	return l
}()

// line is an item's checklist title: name, prices and target.
func line(w watch, p price, traded bool) string {
	s := w.Name
	switch {
	case !traded || (p.High == 0 && p.Low == 0):
		s += " · no trades yet"
	case p.High == 0 || p.Low == 0 || p.High == p.Low:
		s += " · " + coins(mid(p))
	default:
		s += " · " + coins(p.Low) + " – " + coins(p.High)
	}
	if w.Target > 0 {
		s += " · " + target(w)
		switch {
		case w.Hit != nil && w.HitAt > 0:
			s += " (hit " + coins(w.HitAt) + "; untick to watch again)"
		case w.Hit != nil:
			s += " (off)"
		}
	}
	return s
}

func target(w watch) string {
	if w.Above {
		return "alert ≥ " + coins(w.Target)
	}
	return "alert ≤ " + coins(w.Target)
}

// mid is the price to show alone: the newer of the two trades.
func mid(p price) int64 {
	if p.Low == 0 || (p.High != 0 && p.HighTime > p.LowTime) {
		return p.High
	}
	return p.Low
}

func itemURL(id int) string { return "https://prices.runescape.wiki/osrs/item/" + strconv.Itoa(id) }

// coins writes a price the way players do: 162.5m, 1.25b, 900k, 750.
func coins(v int64) string {
	f := float64(v)
	trim := func(s string) string {
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
		return s
	}
	switch {
	case v >= 1e9:
		return trim(strconv.FormatFloat(f/1e9, 'f', 2, 64)) + "b"
	case v >= 1e7:
		return trim(strconv.FormatFloat(f/1e6, 'f', 1, 64)) + "m"
	case v >= 1e6:
		return trim(strconv.FormatFloat(f/1e6, 'f', 2, 64)) + "m"
	case v >= 1e4:
		return trim(strconv.FormatFloat(f/1e3, 'f', 1, 64)) + "k"
	}
	return strconv.FormatInt(v, 10)
}
