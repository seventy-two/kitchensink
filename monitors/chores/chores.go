// Package chores is a to-do list that fills itself: chores that come round
// every so many days, such as the washing, each due again a set time after
// it was last done. Chores that share something, like the washing machine,
// are spread so a day has no more of them than it allows, each going as
// close to when it's due as it can, and on the weekdays it prefers.
package chores

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("chores", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	Chores []Chore `json:"chores"`
	// PerDay is how many chores using each thing a day can take, e.g.
	// {"washer": 2}. Anything not listed takes one a day.
	PerDay map[string]int `json:"perDay"`
	// Timezone is the IANA zone days are counted in.
	Timezone string `json:"timezone"`
	// RemindAt is the local time ("08:00") of a notification listing the
	// day's chores, or empty for none.
	RemindAt string `json:"remindAt"`
}

// Chore is one recurring chore.
type Chore struct {
	// ID keeps its history; it defaults to the name in lower case with
	// dashes, so set it before renaming a chore.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Every is how many days after it's done it's due again.
	Every int `json:"every"`
	// Flex is how many days it can be brought forward or put back to fit
	// around other chores before it counts as late. It defaults to 1.
	Flex *int `json:"flex"`
	// Days are the weekdays it prefers, e.g. ["Sat", "Sun"]; empty for any.
	Days []string `json:"days"`
	// Uses names what it shares with other chores, e.g. "washer".
	Uses string `json:"uses"`
}

// chore is a Chore checked and ready to plan.
type chore struct {
	id, name string
	every    int
	flex     int
	days     [7]bool
	any      bool
	uses     string
	// reorder marks a chore tracked by an action rather than configured,
	// such as buying something again.
	reorder bool
}

// Monitor is the chores list.
type Monitor struct {
	chores   []chore
	perDay   map[string]int
	loc      *time.Location
	remindAt time.Duration // after midnight; negative for none
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Timezone: "Europe/London", RemindAt: "08:00"}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone: %w", err)
	}
	m := &Monitor{perDay: c.PerDay, loc: loc, remindAt: -1}
	if c.RemindAt != "" {
		t, err := time.Parse("15:04", c.RemindAt)
		if err != nil {
			return nil, fmt.Errorf("remindAt must be a time like \"08:00\"")
		}
		m.remindAt = time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	}
	for k, n := range c.PerDay {
		if n < 1 {
			return nil, fmt.Errorf("perDay %q must be at least 1", k)
		}
	}
	if len(c.Chores) == 0 {
		return nil, fmt.Errorf("no chores")
	}
	seen := map[string]bool{}
	for _, ch := range c.Chores {
		k := chore{id: ch.ID, name: strings.TrimSpace(ch.Name), every: ch.Every, flex: 1, uses: ch.Uses, any: len(ch.Days) == 0}
		if k.name == "" {
			return nil, fmt.Errorf("a chore needs a name")
		}
		if k.id == "" {
			k.id = slug(k.name)
		}
		if seen[k.id] {
			return nil, fmt.Errorf("two chores have the ID %q", k.id)
		}
		seen[k.id] = true
		if k.every < 1 {
			return nil, fmt.Errorf("%s: every must be at least 1 (days)", k.name)
		}
		if ch.Flex != nil {
			k.flex = *ch.Flex
		}
		if k.flex < 0 || k.flex >= k.every {
			return nil, fmt.Errorf("%s: flex must be from 0 to every-1", k.name)
		}
		for _, d := range ch.Days {
			w, ok := weekday(d)
			if !ok {
				return nil, fmt.Errorf("%s: unknown day %q", k.name, d)
			}
			k.days[w] = true
		}
		m.chores = append(m.chores, k)
	}
	return m, nil
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}

func weekday(s string) (time.Weekday, bool) {
	s = strings.ToLower(s)
	for w := time.Sunday; w <= time.Saturday; w++ {
		if len(s) >= 3 && strings.HasPrefix(strings.ToLower(w.String()), s) {
			return w, true
		}
	}
	return 0, false
}

// record is what's kept about a chore.
type record struct {
	// Done is the days it was done, oldest first.
	Done []string `json:"done,omitempty"`
	// PutOff is the earliest day it can go, when it's been put off.
	PutOff string `json:"putOff,omitempty"`
}

// keepDone is how many days done each chore remembers.
const keepDone = 20

// State keys: each chore's record, by ID; the chores tracked by actions;
// and the IDs of those stopped, which aren't tracked again.
const (
	recordsKey = "records"
	trackedKey = "tracked"
	stoppedKey = "stopped"
)

// Tracked is a chore added by a "track" action, e.g. reordering something
// bought regularly: the action's ID is its ID, and its Data this.
type Tracked struct {
	Name  string `json:"name"`
	Every int    `json:"every"`
	// Last, in a track action, is a day it was done ("2006-01-02").
	Last string `json:"last,omitempty"`
}

// Done is the Data of a "done" action: the day the chore with the action's
// ID was done, which can be in the past.
type Done struct {
	Day string `json:"day"`
}

// planned is a chore and the day it's planned for.
type planned struct {
	chore
	on    day
	due   day
	never bool
	done  bool // done today
}

// label names the chore in a summary or reminder.
func (p planned) label() string {
	if p.reorder {
		r := []rune(p.name)
		return "Reorder " + strings.ToLower(string(r[:1])) + string(r[1:])
	}
	return p.name
}

// lateBy is how many days past its flex it is on its planned day.
func (p planned) lateBy() int {
	if p.never {
		return 0
	}
	return max(0, int(p.on-p.due)-p.flex)
}

// view is the plan as of today.
type view struct {
	today   day
	records map[string]*record
	items   []planned // in config order, then tracked ones by ID
	tracked map[string]Tracked
}

func (m *Monitor) view(env monitor.Env) (view, error) {
	v := view{today: dayOf(env.Now(), m.loc), records: map[string]*record{}, tracked: map[string]Tracked{}}
	if _, err := env.State.Get(recordsKey, &v.records); err != nil {
		return v, fmt.Errorf("reading chores: %w", err)
	}
	if _, err := env.State.Get(trackedKey, &v.tracked); err != nil {
		return v, fmt.Errorf("reading tracked chores: %w", err)
	}
	all := slices.Clone(m.chores)
	for _, id := range slices.Sorted(maps.Keys(v.tracked)) {
		t := v.tracked[id]
		all = append(all, chore{id: id, name: t.Name, every: t.Every, any: true, reorder: true})
	}
	var jobs []job
	used := map[string]map[day]int{}
	for i, c := range all {
		r := v.records[c.id]
		if r == nil {
			r = &record{}
			v.records[c.id] = r
		}
		p := planned{chore: c, due: v.today, never: true}
		if n := len(r.Done); n > 0 {
			if last, ok := parseDay(r.Done[n-1]); ok {
				p.due, p.never = last+day(c.every), false
				p.done = last == v.today
			}
		}
		if p.done {
			p.on = v.today
			if c.uses != "" {
				if used[c.uses] == nil {
					used[c.uses] = map[day]int{}
				}
				used[c.uses][v.today]++
			}
		} else {
			from := v.today
			if d, ok := parseDay(r.PutOff); ok && d > from {
				from = d
			}
			jobs = append(jobs, job{idx: i, due: p.due, flex: c.flex, from: from, days: c.days, any: c.any, uses: c.uses, never: p.never})
		}
		v.items = append(v.items, p)
	}
	for i, d := range plan(jobs, used, m.perDay) {
		v.items[i].on = d
	}
	return v, nil
}

func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	v, err := m.view(env)
	if err != nil {
		return err
	}
	if a.Type == "track" {
		return m.track(env, v, a)
	}
	i := slices.IndexFunc(v.items, func(p planned) bool { return p.id == a.ID })
	if i < 0 {
		return fmt.Errorf("%w: no chore %q", monitor.ErrBadAction, a.ID)
	}
	p := v.items[i]
	r := v.records[p.id]
	switch a.Type {
	case "toggle":
		// Done today, or not done after all.
		if p.done {
			r.Done = r.Done[:len(r.Done)-1]
		} else {
			r.done(v.today)
		}
	case "done":
		var d Done
		if err := json.Unmarshal(a.Data, &d); err != nil {
			return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
		}
		on, ok := parseDay(d.Day)
		if !ok || on > v.today {
			return fmt.Errorf("%w: %q isn't a day so far", monitor.ErrBadAction, d.Day)
		}
		r.done(on)
	case "delete":
		if p.reorder {
			// Stop tracking it, for good.
			return m.stop(env, v, p.id)
		}
		// Otherwise clients' delete button puts it off a day.
		if p.done {
			return fmt.Errorf("%w: %s is done today", monitor.ErrBadAction, p.name)
		}
		r.PutOff = (p.on + 1).String()
	default:
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	return env.State.Set(recordsKey, v.records)
}

// done records the chore done on a day, keeping the days in order.
func (r *record) done(on day) {
	s := on.String()
	if slices.Contains(r.Done, s) {
		return
	}
	r.Done = append(r.Done, s)
	slices.Sort(r.Done) // the format sorts by date
	if n := len(r.Done); n > keepDone {
		r.Done = r.Done[n-keepDone:]
	}
	if r.Done[len(r.Done)-1] == s {
		r.PutOff = ""
	}
}

// maxEvery bounds a tracked chore's interval, in days.
const maxEvery = 730

// track adds or updates a tracked chore, unless it's been stopped.
func (m *Monitor) track(env monitor.Env, v view, a monitor.Action) error {
	var t Tracked
	if err := json.Unmarshal(a.Data, &t); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	t.Name = strings.TrimSpace(t.Name)
	if a.ID == "" || t.Name == "" || t.Every < 1 || t.Every > maxEvery {
		return fmt.Errorf("%w: tracking needs an ID, a name and every from 1 to %d days", monitor.ErrBadAction, maxEvery)
	}
	if slices.ContainsFunc(m.chores, func(c chore) bool { return c.id == a.ID }) {
		return fmt.Errorf("%w: %q is a configured chore", monitor.ErrBadAction, a.ID)
	}
	var stopped []string
	if _, err := env.State.Get(stoppedKey, &stopped); err != nil {
		return err
	}
	if slices.Contains(stopped, a.ID) {
		return nil
	}
	if t.Last != "" {
		on, ok := parseDay(t.Last)
		if !ok || on > v.today {
			return fmt.Errorf("%w: %q isn't a day so far", monitor.ErrBadAction, t.Last)
		}
		r := v.records[a.ID]
		if r == nil {
			r = &record{}
			v.records[a.ID] = r
		}
		r.done(on)
		if err := env.State.Set(recordsKey, v.records); err != nil {
			return err
		}
	}
	v.tracked[a.ID] = Tracked{Name: t.Name, Every: t.Every}
	return env.State.Set(trackedKey, v.tracked)
}

// stop stops tracking a chore and forgets it.
func (m *Monitor) stop(env monitor.Env, v view, id string) error {
	var stopped []string
	if _, err := env.State.Get(stoppedKey, &stopped); err != nil {
		return err
	}
	if err := env.State.Set(stoppedKey, append(stopped, id)); err != nil {
		return err
	}
	delete(v.tracked, id)
	delete(v.records, id)
	if err := env.State.Set(recordsKey, v.records); err != nil {
		return err
	}
	return env.State.Set(trackedKey, v.tracked)
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	v, err := m.view(env)
	if err != nil {
		return monitor.Result{}, err
	}
	now := env.Now().In(m.loc)

	// Today's first, open before done; then the rest by day.
	items := slices.Clone(v.items)
	slices.SortStableFunc(items, func(a, b planned) int {
		if a.on != b.on {
			return int(a.on - b.on)
		}
		if a.done != b.done {
			if a.done {
				return 1
			}
			return -1
		}
		return 0
	})
	today := monitor.Block{Type: monitor.BlockChecklist, Title: "Today", Items: []monitor.Item{}, DeleteHint: "Put off a day"}
	later := monitor.Block{Type: monitor.BlockChecklist, Title: "Coming up", Items: []monitor.Item{}, DeleteHint: "Put off a day"}
	reorders := monitor.Block{Type: monitor.BlockChecklist, Title: "Reorders", Items: []monitor.Item{}, DeleteHint: "Stop tracking"}
	var open []planned
	for _, p := range items {
		it := monitor.Item{ID: p.id, Title: p.name, Done: p.done}
		if p.reorder {
			// Due means likely running out; ticking it means ordered.
			switch {
			case p.done:
				it.Title += " · ordered today"
			case p.on == v.today:
				it.Title += " · due"
				it.Tone = monitor.ToneWarn
				open = append(open, p)
			default:
				it.Title += " · " + when(p.on, v.today)
			}
			reorders.Items = append(reorders.Items, it)
			continue
		}
		if p.on == v.today {
			if late := p.lateBy(); late > 0 {
				it.Title += fmt.Sprintf(" · %s late", days(late))
				it.Tone = monitor.ToneWarn
			}
			today.Items = append(today.Items, it)
			if !p.done {
				open = append(open, p)
			}
		} else {
			it.Title += " · " + when(p.on, v.today)
			later.Items = append(later.Items, it)
		}
	}

	blocks := []monitor.Block{today, later}
	if len(reorders.Items) > 0 {
		blocks = append(blocks, reorders)
	}
	res := monitor.Result{Blocks: blocks, Summary: summary(open, items, v.today)}
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, m.loc)
	res.Next = midnight.Sub(now)
	if m.remindAt >= 0 {
		at := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.loc).Add(m.remindAt)
		if !now.Before(at) && len(open) > 0 {
			var names []string
			for _, p := range open {
				names = append(names, p.label())
			}
			res.Events = append(res.Events, monitor.Event{
				Key:      "today/" + v.today.String(),
				Time:     now,
				Title:    "Chores today",
				Body:     strings.Join(names, ", "),
				Priority: monitor.PriorityNormal,
			})
		}
		if now.Before(at) {
			res.Next = at.Sub(now)
		}
	}
	res.Next += time.Second
	return res, nil
}

// summary is today's open chores, or the next one when there are none.
func summary(open, items []planned, today day) []monitor.Stat {
	var out []monitor.Stat
	for i, p := range open {
		s := monitor.Stat{Value: p.label()}
		if i == 0 {
			s.Label = "Today"
		}
		if p.lateBy() > 0 {
			s.Tone = monitor.ToneWarn
		}
		out = append(out, s)
	}
	if len(out) > 0 {
		return out
	}
	out = append(out, monitor.Stat{Label: "Today", Value: "All done", Tone: monitor.ToneGood})
	for _, p := range items {
		if p.on > today {
			out = append(out, monitor.Stat{Label: "Next", Value: p.label(), Detail: when(p.on, today)})
			break
		}
	}
	return out
}

// when names a day after today: tomorrow, a weekday within the week, or a
// date.
func when(d, today day) string {
	switch {
	case d == today+1:
		return "tomorrow"
	case d < today+7:
		return d.date().Format("Mon")
	default:
		return d.date().Format("Mon 2 Jan")
	}
}

func days(n int) string {
	if n == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", n)
}
