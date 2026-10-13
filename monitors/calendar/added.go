package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Added is an event given to the calendar by an action rather than read
// from a feed, e.g. a booking another monitor found in email. It's the
// Data of an "upsert" action; a "remove" action with its Key as the ID
// takes it away again, e.g. when the booking's cancelled.
type Added struct {
	// Key identifies it, so a later upsert with the same key replaces it.
	Key   string `json:"key"`
	Title string `json:"title"`
	// Start and End are when it's on. An all-day event's are days in the
	// calendar's timezone, End being the day after it finishes (or zero
	// for one day). A timed event's End can be zero, for just a start.
	Start    time.Time `json:"start"`
	End      time.Time `json:"end,omitzero"`
	AllDay   bool      `json:"allDay,omitempty"`
	Location string    `json:"location,omitempty"`
	// Detail goes under the title in place of a note from Gemini.
	Detail string `json:"detail,omitempty"`
	// Source is shown as its calendar's name, e.g. "Email".
	Source string `json:"source,omitempty"`
}

// addedKey is the state key holding added events, by key.
const addedKey = "added"

// addedTone colours added events' source, to tell them from the feeds'.
const addedTone = monitor.ToneWarn

func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	added, err := loadAdded(env)
	if err != nil {
		return err
	}
	switch a.Type {
	case "upsert":
		var e Added
		if err := json.Unmarshal(a.Data, &e); err != nil {
			return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
		}
		e.Title = strings.TrimSpace(e.Title)
		if e.Key == "" || e.Title == "" || e.Start.IsZero() {
			return fmt.Errorf("%w: an event needs a key, title and start", monitor.ErrBadAction)
		}
		if !e.End.IsZero() && e.End.Before(e.Start) {
			return fmt.Errorf("%w: %s ends before it starts", monitor.ErrBadAction, e.Title)
		}
		if e.Source == "" {
			e.Source = "Added"
		}
		added[e.Key] = e
	case "remove":
		if _, ok := added[a.ID]; !ok {
			return fmt.Errorf("%w: no event %q", monitor.ErrBadAction, a.ID)
		}
		delete(added, a.ID)
	default:
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	return env.State.Set(addedKey, added)
}

func loadAdded(env monitor.Env) (map[string]Added, error) {
	added := map[string]Added{}
	if _, err := env.State.Get(addedKey, &added); err != nil {
		return nil, fmt.Errorf("reading added events: %w", err)
	}
	return added, nil
}

// addedOccurrences are the added events overlapping [from, to). Ones over
// for more than a day are forgotten.
func (m *Monitor) addedOccurrences(env monitor.Env, from, to time.Time) ([]occurrence, error) {
	added, err := loadAdded(env)
	if err != nil {
		return nil, err
	}
	var out []occurrence
	pruned := false
	for k, e := range added {
		o := occurrence{Cal: -1, UID: "added:" + e.Key, Title: e.Title, Location: e.Location,
			Start: e.Start.In(m.loc), End: e.End.In(m.loc), AllDay: e.AllDay, source: e.Source, detail: e.Detail}
		if e.AllDay {
			o.Start = midnight(time.Date(e.Start.Year(), e.Start.Month(), e.Start.Day(), 0, 0, 0, 0, m.loc))
			if e.End.IsZero() || !e.End.After(e.Start) {
				o.End = o.Start.AddDate(0, 0, 1)
			} else {
				o.End = time.Date(e.End.Year(), e.End.Month(), e.End.Day(), 0, 0, 0, 0, m.loc)
			}
		} else if e.End.IsZero() {
			o.End = o.Start
		}
		if o.End.Before(from.AddDate(0, 0, -1)) {
			delete(added, k)
			pruned = true
			continue
		}
		if overlaps(o, from, to) {
			out = append(out, o)
		}
	}
	if pruned {
		if err := env.State.Set(addedKey, added); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// calName and calTone are the name and tone of o's calendar, or of its
// source if it was added.
func (m *Monitor) calName(o occurrence) string {
	if o.Cal < 0 {
		return o.source
	}
	return m.cfg.Calendars[o.Cal].Name
}

func (m *Monitor) calTone(o occurrence) monitor.Tone {
	if o.Cal < 0 {
		return addedTone
	}
	return m.cfg.Calendars[o.Cal].Tone
}
