// Package calendar shows what's coming up across iCalendar (ICS) feeds,
// such as Google Calendar's or Outlook's private calendar addresses, and
// reminds you shortly before each event starts.
package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("calendar", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Calendar is one feed.
type Calendar struct {
	// Name is shown beside its events, e.g. "Work".
	Name string `json:"name"`
	// URL is the feed's address. These are secret (anyone with one can read
	// the calendar), so keep them in the environment with "${NAME}".
	// webcal:// addresses are fetched over https.
	URL string `json:"url"`
	// Tone colours the calendar's name, to tell calendars apart.
	Tone monitor.Tone `json:"tone"`
}

// Config is the monitor's config.
type Config struct {
	Calendars []Calendar `json:"calendars"`
	// Timezone is the IANA zone days and times are shown in, and the zone
	// of all-day and floating events. It defaults to Europe/London.
	Timezone string `json:"timezone"`
	// Days is how many days to show, starting today. It defaults to 7.
	Days int `json:"days"`
	// RemindMinutes notifies this long before each timed event. It
	// defaults to 10; 0 turns reminders off.
	RemindMinutes *int `json:"remindMinutes"`
	// MaxEvents caps the rows shown across all days. It defaults to 40.
	MaxEvents int `json:"maxEvents"`
	// Explain, if set, adds a note from Gemini under each event shown
	// saying what it is, when and where.
	Explain *Explain `json:"explain"`
}

// Monitor is the calendar monitor.
type Monitor struct {
	cfg    Config
	loc    *time.Location
	remind time.Duration
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Timezone: "Europe/London", Days: 7, MaxEvents: 40}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Calendars) == 0 {
		return nil, fmt.Errorf("calendar needs at least one calendar")
	}
	for i, cal := range c.Calendars {
		if cal.URL == "" {
			return nil, fmt.Errorf("calendar %d has no url", i)
		}
		if cal.Name == "" {
			c.Calendars[i].Name = fmt.Sprintf("Calendar %d", i+1)
		}
		switch cal.Tone {
		case monitor.ToneNone, monitor.ToneGood, monitor.ToneBad, monitor.ToneWarn, monitor.ToneMuted, monitor.ToneAccent:
		default:
			return nil, fmt.Errorf("calendar %q: unknown tone %q", c.Calendars[i].Name, cal.Tone)
		}
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	if c.Days < 1 {
		return nil, fmt.Errorf("days must be at least 1")
	}
	if e := c.Explain; e != nil {
		if e.APIKey == "" {
			return nil, fmt.Errorf("explain needs an apiKey")
		}
		if e.Model == "" {
			e.Model = defaultModel
		}
		if e.URL == "" {
			e.URL = defaultExplainURL
		}
	}
	m := &Monitor{cfg: c, loc: loc, remind: 10 * time.Minute}
	if c.RemindMinutes != nil {
		m.remind = time.Duration(*c.RemindMinutes) * time.Minute
	}
	return m, nil
}

// occurrence is one instance of an event.
type occurrence struct {
	Cal         int // index into Config.Calendars
	UID         string
	Title       string
	Location    string
	Description string
	Link        string // a video call link, if the event has one
	Start, End  time.Time
	AllDay      bool
	// For added events (Cal -1): what to show as the calendar, and the
	// line under the title.
	source, detail string
}

// slack lets a reminder fire when a poll wakes a moment before it's due.
const slack = time.Minute

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now().In(m.loc)
	today := midnight(now)
	end := today.AddDate(0, 0, m.cfg.Days)

	var (
		occs   []occurrence
		failed []string
	)
	for i, cal := range m.cfg.Calendars {
		events, err := fetch(ctx, env.HTTP, cal)
		if err != nil {
			env.Log.Warn("calendar fetch failed", "calendar", cal.Name, "err", err)
			failed = append(failed, err.Error())
			continue
		}
		occs = append(occs, expandEvents(events, i, m.loc, today, end)...)
	}
	if len(failed) == len(m.cfg.Calendars) {
		return monitor.Result{}, errors.New(strings.Join(failed, "; "))
	}
	extra, err := m.addedOccurrences(env, today, end)
	if err != nil {
		return monitor.Result{}, err
	}
	occs = append(occs, withoutCopies(extra, occs)...)
	sort.SliceStable(occs, func(i, j int) bool {
		a, b := occs[i], occs[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		return a.Title < b.Title
	})

	var notes map[string]string
	if m.cfg.Explain != nil {
		notes = m.explain(ctx, env, occs, now)
	}
	for _, o := range occs {
		if o.Cal < 0 {
			if notes == nil {
				notes = map[string]string{}
			}
			// The source goes with the detail, not in a column of its own,
			// which would squeeze the titles on a phone.
			notes[m.noteKey(o)] = strings.Join(slices.DeleteFunc([]string{o.source, o.detail}, func(s string) bool { return s == "" }), " · ")
		}
	}

	var blocks []monitor.Block
	for _, f := range failed {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: "Couldn't load " + f})
	}
	blocks = append(blocks, m.blocks(occs, now, notes)...)
	return monitor.Result{
		Blocks:   blocks,
		Summary:  m.summary(occs, now, notes),
		Events:   m.reminders(occs, now),
		Next:     m.next(occs, now),
		Relevant: relevant(occs, now),
	}, nil
}

// lead is how long before an event the panel becomes relevant.
const lead = time.Hour

// relevant is when the panel matters most: from an hour before the
// current or next timed event until it ends. All-day events don't count.
func relevant(occs []occurrence, now time.Time) monitor.Window {
	for _, o := range occs {
		if o.AllDay || over(o, now) {
			continue
		}
		end := o.End
		if !end.After(o.Start) {
			end = o.Start.Add(15 * time.Minute)
		}
		return monitor.Window{From: o.Start.Add(-lead), Until: end}
	}
	return monitor.Window{}
}

// summary is the next event: when, what, and its note (or, without one,
// which calendar it's on, if there's more than one), led by what's on now
// if anything is. Today's all-day events show only when nothing else is
// coming up.
func (m *Monitor) summary(occs []occurrence, now time.Time, notes map[string]string) []monitor.Stat {
	today, tomorrow := midnight(now), midnight(now).AddDate(0, 0, 1)
	detail := func(o occurrence) string {
		if n := notes[m.noteKey(o)]; n != "" {
			return n
		}
		if len(m.cfg.Calendars) > 1 || o.Cal < 0 {
			return m.calName(o)
		}
		return ""
	}
	var current, next, allDay *occurrence
	for i := range occs {
		o := &occs[i]
		switch {
		case over(*o, now):
		case o.AllDay && o.Start.Before(tomorrow):
			if allDay == nil {
				allDay = o
			}
		case !o.AllDay && !o.Start.After(now):
			if current == nil {
				current = o
			}
		case next == nil:
			next = o
		}
	}
	var out []monitor.Stat
	if current != nil {
		out = append(out, monitor.Stat{Label: "Now, until " + current.End.In(m.loc).Format("15:04"), Value: current.Title,
			Detail: detail(*current), Tone: monitor.ToneAccent})
	}
	if next == nil && current == nil {
		next = allDay
	}
	if next != nil {
		label := when(*next, now, today)
		if next == allDay {
			label = "Today"
		}
		out = append(out, monitor.Stat{Label: label, Value: next.Title, Detail: detail(*next)})
	}
	if len(out) == 0 {
		out = append(out, monitor.Stat{Label: "Next", Value: fmt.Sprintf("Nothing in the next %d days", m.cfg.Days), Tone: monitor.ToneMuted})
	}
	return out
}

// when is a short label for when o starts: "in 25 min", "14:00", "Tomorrow
// 09:00", "Thu 09:00", or the day alone for all-day events.
func when(o occurrence, now, today time.Time) string {
	start := o.Start.In(today.Location())
	day := int(midnight(start).Sub(today).Hours()/24 + 0.5)
	switch {
	case o.AllDay && day == 1:
		return "Tomorrow"
	case o.AllDay:
		return start.Format("Mon 2 Jan")
	case start.Sub(now) < time.Hour:
		return fmt.Sprintf("in %d min", max(1, int(start.Sub(now).Minutes()+0.5)))
	case day == 0:
		return start.Format("15:04")
	case day == 1:
		return "Tomorrow " + start.Format("15:04")
	}
	return start.Format("Mon 15:04")
}

// blocks makes a table for each day with something on. Today always gets
// one, so an empty day says so. notes, by noteKey, go under the events'
// titles.
func (m *Monitor) blocks(occs []occurrence, now time.Time, notes map[string]string) []monitor.Block {
	today := midnight(now)
	// No headings: each day's table is short, and they'd repeat.
	cols := []monitor.Column{{}, {Fill: true}}
	many := len(m.cfg.Calendars) > 1
	if many {
		cols = append(cols, monitor.Column{})
	}

	var out []monitor.Block
	rows := 0
	for i := 0; i < m.cfg.Days && rows < m.cfg.MaxEvents; i++ {
		day := today.AddDate(0, 0, i)
		next := today.AddDate(0, 0, i+1)
		b := monitor.Block{Type: monitor.BlockTable, Title: dayTitle(day, i), Columns: cols, Rows: [][]monitor.Cell{}}
		for _, o := range occs {
			if !overlaps(o, day, next) {
				continue
			}
			if rows >= m.cfg.MaxEvents {
				break
			}
			rows++
			tone := monitor.ToneNone
			switch {
			case over(o, now):
				tone = monitor.ToneMuted
			case !o.AllDay && !o.Start.After(now):
				tone = monitor.ToneAccent // on now
			}
			title := o.Title
			if place := shortPlace(o); place != "" {
				title += " · " + place
			}
			row := []monitor.Cell{
				{Text: timeLabel(o, day, next), Tone: monitor.ToneMuted},
				{Text: title, Tone: tone, URL: o.Link},
			}
			if many {
				name, tone := m.calName(o), m.calTone(o)
				if o.Cal < 0 {
					name = "" // said in its detail
				}
				row = append(row, monitor.Cell{Text: name, Tone: tone})
			}
			row[1].Detail = notes[m.noteKey(o)]
			b.Rows = append(b.Rows, row)
		}
		switch {
		case len(b.Rows) > 0:
			out = append(out, b)
		case i == 0:
			out = append(out, monitor.Block{Type: monitor.BlockText, Title: b.Title, Text: "Nothing on today."})
		}
	}
	return out
}

// reminders raises an event for each timed occurrence starting within the
// reminder time. Keys hold the start, so a moved meeting reminds again.
func (m *Monitor) reminders(occs []occurrence, now time.Time) []monitor.Event {
	if m.remind <= 0 {
		return nil
	}
	var out []monitor.Event
	for _, o := range occs {
		until := o.Start.Sub(now)
		if o.AllDay || until <= 0 || until > m.remind+slack {
			continue
		}
		body := []string{inMinutes(until), m.calName(o)}
		if o.Location != "" && o.Location != o.Link {
			body = append(body, o.Location)
		}
		out = append(out, monitor.Event{
			Key:   "remind:" + m.calName(o) + ":" + o.UID + ":" + o.Start.UTC().Format(time.RFC3339),
			Time:  now,
			Title: o.Title + " at " + o.Start.In(m.loc).Format("15:04"),
			Body:  strings.Join(body, " · "),
			URL:   o.Link,
		})
	}
	return out
}

// next asks for a poll when the next reminder is due, or at midnight so the
// days move on, whichever is sooner.
func (m *Monitor) next(occs []occurrence, now time.Time) time.Duration {
	soonest := midnight(now).AddDate(0, 0, 1).Sub(now)
	if m.remind > 0 {
		for _, o := range occs {
			if o.AllDay {
				continue
			}
			if d := o.Start.Add(-m.remind).Sub(now); d > slack && d < soonest {
				soonest = d
			}
		}
	}
	return max(soonest, 30*time.Second)
}

// maxBody bounds a feed's size; Google's include years of history.
const maxBody = 32 << 20

// fetch downloads and parses a feed. Errors name the calendar but never
// its URL, which is a secret.
func fetch(ctx context.Context, client *http.Client, cal Calendar) ([]vevent, error) {
	u := cal.URL
	if rest, ok := strings.CutPrefix(u, "webcal://"); ok {
		u = "https://" + rest
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: bad url", cal.Name)
	}
	resp, err := client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s: %v", cal.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: feed returned %s", cal.Name, resp.Status)
	}
	events, err := parseICS(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%s: reading feed: %v", cal.Name, err)
	}
	return events, nil
}

// expandEvents turns a feed's events into the occurrences overlapping
// [from, to), applying recurrence rules, exceptions and moved or cancelled
// instances.
func expandEvents(events []vevent, cal int, loc *time.Location, from, to time.Time) []occurrence {
	// Instances moved or cancelled by an override, by UID and original
	// start.
	overridden := map[string]bool{}
	for _, e := range events {
		if rid, ok := e.get("RECURRENCE-ID"); ok {
			if t, _, err := parseTime(rid, loc); err == nil {
				overridden[instanceKey(e.text("UID"), t)] = true
			}
		}
	}

	var out []occurrence
	keep := func(o occurrence) {
		if overlaps(o, from, to) {
			out = append(out, o)
		}
	}
	for _, e := range events {
		if strings.EqualFold(e.text("STATUS"), "CANCELLED") {
			continue
		}
		base, ok := baseOccurrence(e, cal, loc)
		if !ok {
			continue
		}
		rr, hasRule := e.get("RRULE")
		_, isOverride := e.get("RECURRENCE-ID")
		_, hasRDate := e.get("RDATE")
		if isOverride || (!hasRule && !hasRDate) {
			keep(base)
			continue
		}

		starts := []time.Time{base.Start}
		dur := base.End.Sub(base.Start)
		if hasRule {
			if rule, err := parseRRule(rr.Value, loc); err == nil {
				starts = rule.expand(base.Start, from.Add(-dur), to)
			}
		}
		for _, p := range e["RDATE"] {
			starts = append(starts, parseTimes(p, loc)...)
		}
		excluded := map[int64]bool{}
		for _, p := range e["EXDATE"] {
			for _, t := range parseTimes(p, loc) {
				excluded[t.Unix()] = true
			}
		}
		for _, s := range starts {
			if excluded[s.Unix()] || overridden[instanceKey(base.UID, s)] {
				continue
			}
			o := base
			o.Start = s
			if o.AllDay {
				// Keep all-day events whole days across clock changes.
				o.End = s.AddDate(0, 0, int(dur.Round(24*time.Hour)/(24*time.Hour)))
			} else {
				o.End = s.Add(dur)
			}
			keep(o)
		}
	}
	return out
}

// baseOccurrence reads an event's own start, end and details.
func baseOccurrence(e vevent, cal int, loc *time.Location) (occurrence, bool) {
	sp, ok := e.get("DTSTART")
	if !ok {
		return occurrence{}, false
	}
	start, allDay, err := parseTime(sp, loc)
	if err != nil {
		return occurrence{}, false
	}
	end := start
	if allDay {
		end = start.AddDate(0, 0, 1)
	}
	if ep, ok := e.get("DTEND"); ok {
		if t, _, err := parseTime(ep, loc); err == nil && !t.Before(start) {
			end = t
		}
	} else if dp, ok := e.get("DURATION"); ok {
		if d, err := parseDuration(dp.Value); err == nil && d >= 0 {
			if allDay && d%(24*time.Hour) == 0 {
				end = start.AddDate(0, 0, int(d/(24*time.Hour)))
			} else {
				end = start.Add(d)
			}
		}
	}
	title := strings.TrimSpace(e.text("SUMMARY"))
	if title == "" {
		title = "(No title)"
	}
	o := occurrence{
		Cal:         cal,
		UID:         e.text("UID"),
		Title:       title,
		Location:    strings.TrimSpace(e.text("LOCATION")),
		Description: e.text("DESCRIPTION"),
		Start:       start,
		End:         end,
		AllDay:      allDay,
	}
	for _, field := range []string{"LOCATION", "URL", "X-GOOGLE-CONFERENCE", "DESCRIPTION"} {
		if link := meetingLink.FindString(e.text(field)); link != "" {
			o.Link = strings.TrimRight(link, ".,;)>]")
			break
		}
	}
	if strings.HasPrefix(o.Location, "Microsoft Teams Meeting") {
		o.Location = "" // Outlook's placeholder location for a Teams call
	}
	return o, true
}

// meetingLink finds a video call link in an event's text.
var meetingLink = regexp.MustCompile(`https://(?:meet\.google\.com|teams\.microsoft\.com|teams\.live\.com|[a-z0-9.-]*zoom\.us)/[^\s"'<>\\]+`)

func instanceKey(uid string, start time.Time) string {
	return fmt.Sprintf("%s@%d", uid, start.Unix())
}

// over reports whether o has finished. An event with no length is over
// once it has started.
func over(o occurrence, now time.Time) bool {
	if o.End.Equal(o.Start) {
		return o.Start.Before(now)
	}
	return !o.End.After(now)
}

// overlaps reports whether o is on during [from, to). An event with no
// length counts at its start.
func overlaps(o occurrence, from, to time.Time) bool {
	if !o.Start.Before(to) {
		return false
	}
	return o.End.After(from) || (!o.Start.Before(from) && !o.End.After(o.Start))
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func dayTitle(day time.Time, i int) string {
	date := day.Format("Monday 2 January")
	switch i {
	case 0:
		return "Today · " + date
	case 1:
		return "Tomorrow · " + date
	}
	return date
}

// timeLabel describes when o is on during the day [day, next): "09:00–09:30",
// "All day", or with "…" where it runs on from or into another day.
func timeLabel(o occurrence, day, next time.Time) string {
	if o.AllDay {
		return "All day"
	}
	loc := day.Location()
	from, to := "…", "…"
	if !o.Start.Before(day) {
		from = o.Start.In(loc).Format("15:04")
	}
	if !o.End.After(next) {
		to = o.End.In(loc).Format("15:04")
	}
	if o.End.Equal(o.Start) {
		return from
	}
	if from == "…" && to == "…" {
		return "All day"
	}
	return from + "–" + to
}

func inMinutes(d time.Duration) string {
	n := int((d + 30*time.Second) / time.Minute)
	switch {
	case n < 1:
		return "Starting now"
	case n == 1:
		return "In 1 minute"
	}
	return fmt.Sprintf("In %d minutes", n)
}
