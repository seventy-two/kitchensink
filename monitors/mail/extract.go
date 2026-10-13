package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitors/calendar"
	"github.com/seventy-two/kitchensink/monitors/chores"
)

// Gemini configures the model that reads each email picked out.
type Gemini struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.8-flash.
	Model string `json:"model"`
	// Context tells Gemini a little about you, e.g. who lives with you, to
	// help it judge how long things last.
	Context string `json:"context"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

type gemini struct{ Gemini }

func newGemini(c Gemini) (*gemini, error) {
	if c.APIKey == "" {
		return nil, fmt.Errorf("gemini needs an apiKey")
	}
	if c.Model == "" {
		c.Model = "gemini-3.8-flash"
	}
	if c.URL == "" {
		c.URL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	}
	return &gemini{c}, nil
}

const instruction = `You read one email for someone's personal dashboard and pull out only what belongs on their calendar, to-do list or chores. Most emails, including marketing that merely mentions orders or bookings, give nothing: then return kind "other" and empty lists.

- events: things they'll attend or must be in for, with a date: gigs, matches, films, shows and other tickets; restaurant, hotel and travel bookings (each flight or train leg its own event); appointments (doctor, dentist, haircut, car service); grocery deliveries and other booked delivery or collection slots. Not ordinary parcel deliveries. title is short and specific, as they'd write it ("Vildhjarta", "Dentist", "Waitrose delivery", "Flight to Lisbon"). date is YYYY-MM-DD; start and end are 24-hour HH:MM in UK time (convert if the email gives another zone), empty if unknown (end may be empty); endDate only for stays over several days (the last day). location is the venue and town, or empty. detail is one short line of what's useful on the day, separated by " · " (e.g. "2 tickets · doors 18:30 · ref K8X2P1"), or empty. key identifies the booking across emails, so a confirmation, a later change and a cancellation of the same thing share it: the merchant and its booking or order reference in lower case, plus what each event is if one booking has several, e.g. "easyjet-k8x2p1-outbound"; without a reference, the merchant and date.
- cancelled: keys of events this email cancels, built the same way.
- todos: things they must do by a date that the email asks for and that won't happen otherwise: return an item, collect a parcel from a locker, check in online, pay a balance, send a form. One short line each, with the deadline ("Return the boots to Amazon by Fri 30 Oct"). Never reviews, surveys, upsells or marketing. Only what's still to do as of today.
- chores: IDs from the chore list this email shows were done, e.g. an order confirmation for the chore of placing that order. An email only about an order already placed (dispatch, delivery, a reminder) doesn't count.
- routine: for an order of everyday consumables bought again and again from somewhere other than a supermarket's weekly shop (cleaning products, toiletries, pet food, coffee, filters, batteries, vitamins, contact lenses...), each item as a short generic name in lower case without brand or size ("dishwasher tablets", "cat litter"), with roughly how many days the quantity bought lasts them. Leave out one-off purchases (gadgets, clothes, books, gifts).
- summary: at most six words saying what the email is, e.g. "Vildhjarta tickets", "Amazon order: dishwasher tablets".`

// extraction is what Gemini finds in an email.
type extraction struct {
	Kind      string   `json:"kind"`
	Summary   string   `json:"summary"`
	Events    []xEvent `json:"events"`
	Cancelled []string `json:"cancelled"`
	Todos     []string `json:"todos"`
	Chores    []string `json:"chores"`
	Routine   []xItem  `json:"routine"`
}

type xEvent struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Date     string `json:"date"`
	Start    string `json:"start"`
	End      string `json:"end"`
	EndDate  string `json:"endDate"`
	Location string `json:"location"`
	Detail   string `json:"detail"`
}

type xItem struct {
	Item      string `json:"item"`
	LastsDays int    `json:"lastsDays"`
}

func str() map[string]any { return map[string]any{"type": "string"} }

// described is a string field with a description, which Gemini follows
// more closely than the instruction alone.
func described(d string) map[string]any { return map[string]any{"type": "string", "description": d} }

func list(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"kind":    map[string]any{"type": "string", "enum": []string{"order", "delivery", "booking", "ticket", "appointment", "change", "cancellation", "other"}},
		"summary": str(),
		"events": list(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":      described("merchant and booking reference in lower case, the same in every email about this booking"),
				"title":    described("short and specific, e.g. \"Vildhjarta\", \"Dentist\", \"Waitrose delivery\""),
				"date":     described("the day it's on, YYYY-MM-DD"),
				"start":    described("start time as HH:MM, 24-hour UK time (doors time for a gig), or empty"),
				"end":      described("end time as HH:MM, e.g. the end of a delivery slot, or empty"),
				"endDate":  described("the last day, YYYY-MM-DD, only for stays over several days; else empty"),
				"location": described("venue and town, e.g. \"O2 Academy, Bristol\"; empty for a delivery to home"),
				"detail":   described("one short line of what's useful on the day, parts separated by \" · \", or empty"),
			},
			"required": []string{"key", "title", "date", "start", "end", "endDate", "location", "detail"},
		}),
		"cancelled": list(str()),
		"todos":     list(str()),
		"chores":    list(str()),
		"routine": list(map[string]any{
			"type":       "object",
			"properties": map[string]any{"item": str(), "lastsDays": map[string]any{"type": "integer"}},
			"required":   []string{"item", "lastsDays"},
		}),
	},
	"required": []string{"kind", "summary", "events", "cancelled", "todos", "chores", "routine"},
}

// prompt is the email as Gemini is asked about it.
func (m *Monitor) prompt(h header, text string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today is %s (UK).\n", now.In(m.loc).Format("Monday 2 January 2006"))
	if len(m.choresDone) > 0 && m.chores != "" {
		b.WriteString("Chores they track, by ID, and what email means each was done:\n")
		for _, id := range slices.Sorted(maps.Keys(m.choresDone)) {
			fmt.Fprintf(&b, "- %s: %s\n", id, m.choresDone[id])
		}
	}
	fmt.Fprintf(&b, "\nEmail received %s from %s\nSubject: %s\n\n%s",
		h.Date.In(m.loc).Format("Mon 2 Jan 2006 15:04"), h.From, h.Subject, text)
	return b.String()
}

func (g *gemini) extract(ctx context.Context, client *http.Client, input string) (extraction, error) {
	sys := instruction
	if g.Context != "" {
		sys += "\n\nAbout them: " + g.Context
	}
	body, _ := json.Marshal(map[string]any{
		"model":              g.Model,
		"input":              input,
		"system_instruction": sys,
		"generation_config":  map[string]any{"thinking_level": "medium", "max_output_tokens": 8000},
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": schema},
		"store":              false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL, bytes.NewReader(body))
	if err != nil {
		return extraction{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return extraction{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return extraction{}, fmt.Errorf("returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var in struct {
		Steps []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&in); err != nil {
		return extraction{}, fmt.Errorf("decoding the reply: %w", err)
	}
	var text strings.Builder
	for _, s := range in.Steps {
		if s.Type != "model_output" {
			continue
		}
		for _, c := range s.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
	}
	var x extraction
	if err := json.Unmarshal([]byte(text.String()), &x); err != nil {
		return extraction{}, fmt.Errorf("the reply isn't the JSON asked for: %w (%q)", err, clip(text.String(), 400))
	}
	return x, nil
}

// todoMaxAge is how old an email can be for its to-dos to be added: older
// ones read while catching up have mostly been done or are past.
const todoMaxAge = 14 * 24 * time.Hour

// apply passes what Gemini found on to the other monitors, and says what
// it did.
func (m *Monitor) apply(ctx context.Context, env monitor.Env, h header, x extraction, now time.Time) finding {
	f := finding{Time: h.Date, Account: h.Account, From: h.From, Summary: strings.TrimSpace(x.Summary)}
	if f.Summary == "" {
		f.Summary = h.Subject
	}
	send := func(to string, a monitor.Action, did string) bool {
		if err := act(ctx, env, to, a); err != nil {
			logAct(env.Log, to, a, err)
			return false
		}
		if did != "" {
			f.Did = append(f.Did, did)
		}
		return true
	}
	today := midnight(now.In(m.loc))

	if m.calendar != "" {
		for _, k := range x.Cancelled {
			// Not on the calendar is fine: it may be past or never added.
			if err := act(ctx, env, m.calendar, monitor.Action{Type: "remove", ID: eventKey(k)}); err == nil {
				f.Did = append(f.Did, "Cancelled on the calendar")
			}
		}
		for _, e := range x.Events {
			a, ok := m.calendarEvent(e)
			if !ok || a.End.Before(today) || (a.End.IsZero() && a.Start.Before(today)) {
				continue // unreadable, or over
			}
			data, _ := json.Marshal(a)
			send(m.calendar, monitor.Action{Type: "upsert", Data: data}, "Calendar: "+a.Title+", "+describeWhen(a))
		}
	}
	if m.todo != "" && now.Sub(h.Date) < todoMaxAge {
		for _, t := range x.Todos {
			if t = strings.TrimSpace(t); t != "" {
				send(m.todo, monitor.Action{Type: "add", Text: t}, "To do: "+t)
			}
		}
	}
	if m.chores != "" {
		day := h.Date.In(m.loc).Format(time.DateOnly)
		for _, id := range x.Chores {
			if _, ok := m.choresDone[id]; !ok {
				continue // only the chores it was told about
			}
			data, _ := json.Marshal(chores.Done{Day: day})
			send(m.chores, monitor.Action{Type: "done", ID: id, Data: data}, "Chores: "+id+" done "+h.Date.In(m.loc).Format("Mon 2 Jan"))
		}
		if m.reorders {
			for _, it := range x.Routine {
				if a, did, ok := m.reorder(env, it, h.Date.In(m.loc)); ok {
					send(m.chores, a, did)
				}
			}
		}
	}
	return f
}

// eventKey is the calendar key for a key Gemini gave.
func eventKey(k string) string { return "mail:" + slug(k) }

// calendarEvent turns an event Gemini found into one for the calendar.
func (m *Monitor) calendarEvent(e xEvent) (calendar.Added, bool) {
	day, err := time.ParseInLocation(time.DateOnly, isoDate.FindString(e.Date), m.loc)
	if err != nil || strings.TrimSpace(e.Title) == "" {
		return calendar.Added{}, false
	}
	key := e.Key
	if slug(key) == "" {
		key = e.Title + "-" + e.Date
	}
	a := calendar.Added{Key: eventKey(key), Title: strings.TrimSpace(e.Title), Location: strings.TrimSpace(e.Location),
		Detail: strings.TrimSpace(e.Detail), Source: "Email"}
	clock := func(s string) (time.Duration, bool) {
		hm := clockTime.FindStringSubmatch(s)
		if hm == nil {
			return 0, false
		}
		h, _ := strconv.Atoi(hm[1])
		mi, _ := strconv.Atoi(hm[2])
		return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute, true
	}
	at := func(d time.Time, off time.Duration) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), int(off/time.Hour), int(off%time.Hour/time.Minute), 0, 0, m.loc)
	}
	last := day
	if d, err := time.ParseInLocation(time.DateOnly, isoDate.FindString(e.EndDate), m.loc); err == nil && d.After(day) {
		last = d
	}
	start, timed := clock(e.Start)
	if !timed || last.After(day) {
		// All day, over as many days as it takes.
		a.AllDay, a.Start, a.End = true, day, last.AddDate(0, 0, 1)
		return a, true
	}
	a.Start = at(day, start)
	if end, ok := clock(e.End); ok {
		a.End = at(day, end)
		if !a.End.After(a.Start) {
			a.End = a.End.AddDate(0, 0, 1) // past midnight
		}
	}
	return a, true
}

// Gemini's dates and times, found within whatever else it writes around
// them ("18:30:00.000Z").
var (
	isoDate   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	clockTime = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)`)
)

// describeWhen is when an added event is, briefly.
func describeWhen(a calendar.Added) string {
	if a.AllDay {
		return a.Start.Format("Mon 2 Jan")
	}
	return a.Start.Format("Mon 2 Jan 15:04")
}

// Reordering intervals: days of buying within sameOrder of each other are
// one purchase, and the interval stays within these bounds.
const (
	sameOrder = 3
	minEvery  = 7
	maxEvery  = 365
)

// reorder records a routine item bought on a day and returns the action
// that tracks reordering it: every so many days, from how often it's been
// bought if more than once, else from how long Gemini thinks it lasts.
func (m *Monitor) reorder(env monitor.Env, it xItem, on time.Time) (monitor.Action, string, bool) {
	name := strings.TrimSpace(it.Item)
	id := slug(name)
	if id == "" {
		return monitor.Action{}, "", false
	}
	bought := map[string][]string{}
	if _, err := env.State.Get(boughtKey, &bought); err != nil {
		env.Log.Warn("reading purchases", "err", err)
		return monitor.Action{}, "", false
	}
	day := on.Format(time.DateOnly)
	days := bought[id]
	if !slices.Contains(days, day) {
		days = append(days, day)
		sort.Strings(days)
	}
	if len(days) > 20 {
		days = days[len(days)-20:]
	}
	bought[id] = days
	if err := env.State.Set(boughtKey, bought); err != nil {
		env.Log.Warn("saving purchases", "err", err)
	}

	every := it.LastsDays
	var gaps []int
	var prev time.Time
	for _, d := range days {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			continue
		}
		if !prev.IsZero() {
			if g := int(t.Sub(prev).Hours()/24 + 0.5); g > sameOrder {
				gaps = append(gaps, g)
			} else {
				continue // the same order, or a top-up: measure from the first
			}
		}
		prev = t
	}
	if len(gaps) > 0 {
		slices.Sort(gaps)
		every = gaps[len(gaps)/2]
	}
	every = min(max(every, minEvery), maxEvery)
	r := []rune(name)
	label := string(unicode.ToUpper(r[0])) + string(r[1:])
	data, _ := json.Marshal(chores.Tracked{Name: label, Every: every, Last: days[len(days)-1]})
	return monitor.Action{Type: "track", ID: "reorder-" + id, Data: data},
		fmt.Sprintf("Reorders: %s about every %d days", strings.ToLower(label), every), true
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

func midnight(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
}
