package calendar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Explain configures notes from Gemini on what each upcoming event is.
type Explain struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.8-flash.
	Model string `json:"model"`
	// Context tells Gemini a little about you, to help it guess, e.g.
	// where you live and what you're often up to.
	Context string `json:"context"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

const (
	defaultModel      = "gemini-3.8-flash"
	defaultExplainURL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	// notesKey is the state key holding today's notes, by event.
	notesKey = "notes"
)

const explainInstruction = `You annotate entries on someone's calendar for their dashboard. Titles are often their own shorthand or in-jokes. For each event, work out what is actually happening on its date, using what you know about them and Google Search for what's on: the actual match, film, gig, show or appointment, and where.

Write each note as a short listing, not a sentence: what it is, when, and where, separated by " · ". Name the specifics: the teams, the film, the band; the start, kick-off or doors time (24-hour, UK time); and the venue with its town. For example, an event titled "handegg" on the day the Eagles play the Jaguars in London becomes "Eagles v Jaguars · 14:30 kick-off · Tottenham Hotspur Stadium, London", and a gig follows the pattern "<band> · doors <time> · <venue>, <town>". These show the format only; find each event's real details. Leave out any part you can't find rather than guessing, and add "probably" only when unsure what it is. If you'd only repeat the title and time already shown, as with a private appointment there's nothing to look up for, give an empty note. At most 15 words, no full stop at the end.`

// notesVersion is part of each note's key, so changing how notes are
// written replaces the ones already made.
const notesVersion = "6"

// note is Gemini's line about one event. Day is the date it was made: a
// note for a later day is kept until then, and made again on the day
// itself, when details such as start times are firmest.
type note struct {
	Day  string `json:"day"`
	Text string `json:"text"`
}

// noteKey identifies an occurrence for its note. A change of title, time
// or place gets a fresh one, as does a change to what Gemini is told about
// the user, so editing that redoes every note at once rather than on each
// event's day.
func noteKey(o occurrence, calName, about string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{notesVersion, calName, o.UID, o.Start.UTC().Format(time.RFC3339), o.Title, o.Location, about}, "\x00")))
	return hex.EncodeToString(h[:12])
}

// noteKey is an occurrence's noteKey under this monitor's config.
func (m *Monitor) noteKey(o occurrence) string {
	about := ""
	if m.cfg.Explain != nil {
		about = m.cfg.Explain.Context
	}
	return noteKey(o, m.cfg.Calendars[o.Cal].Name, about)
}

// explain returns notes for the occurrences shown, by noteKey. Gemini is
// asked only about events without a current note (see note); if it fails,
// those just go without until the next poll. Notes for events no longer
// shown are dropped.
func (m *Monitor) explain(ctx context.Context, env monitor.Env, events []occurrence, now time.Time) map[string]string {
	day := now.In(m.loc).Format("2006-01-02")
	today := midnight(now.In(m.loc))
	saved := map[string]note{}
	if _, err := env.State.Get(notesKey, &saved); err != nil {
		env.Log.Warn("reading event notes", "err", err)
	}
	out := map[string]string{}
	kept := map[string]note{}
	var ask []occurrence
	var keys []string
	for _, o := range events {
		k := m.noteKey(o)
		if _, done := kept[k]; done {
			continue // a multi-day event, seen already
		}
		onDay := o.Start.In(m.loc)
		if onDay.Before(today) {
			onDay = today // started earlier and still on
		}
		if n, ok := saved[k]; ok && (n.Day == day || onDay.Format("2006-01-02") > day) {
			out[k] = n.Text
			kept[k] = n
			continue
		}
		ask = append(ask, o)
		keys = append(keys, k)
	}
	if len(ask) > 0 {
		lines, err := m.askGemini(ctx, env.HTTP, ask, now)
		if err != nil {
			env.Log.Warn("explaining events", "err", err)
		}
		// Empty notes are kept too, so they aren't asked about again.
		for i, line := range lines {
			out[keys[i]] = line
			kept[keys[i]] = note{Day: day, Text: line}
		}
	}
	// Saving only the notes in use drops ones for past events.
	if err := env.State.Set(notesKey, kept); err != nil {
		env.Log.Warn("saving event notes", "err", err)
	}
	return out
}

type explainEvent struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	When        string `json:"when"`
	Location    string `json:"location,omitempty"`
	Calendar    string `json:"calendar"`
	Description string `json:"description,omitempty"`
}

// askGemini asks for a line about each event, returned by its index in
// events; ones it didn't answer are missing, and may be asked about again.
func (m *Monitor) askGemini(ctx context.Context, client *http.Client, events []occurrence, now time.Time) (map[int]string, error) {
	cfg := m.cfg.Explain
	var list []explainEvent
	for i, o := range events {
		start := o.Start.In(m.loc)
		when := start.Format("Mon 2 Jan") + ", all day"
		if o.AllDay && o.End.Sub(o.Start) > 24*time.Hour {
			when = start.Format("Mon 2 Jan") + " to " + o.End.AddDate(0, 0, -1).In(m.loc).Format("Mon 2 Jan")
		} else if !o.AllDay {
			when = start.Format("Mon 2 Jan, 15:04") + "–" + o.End.In(m.loc).Format("15:04")
		}
		list = append(list, explainEvent{
			ID: i, Title: o.Title, When: when, Location: o.Location,
			Calendar: m.cfg.Calendars[o.Cal].Name, Description: clip(o.Description, 300),
		})
	}
	eventsJSON, _ := json.Marshal(list)
	input := "Today is " + now.In(m.loc).Format("Monday 2 January 2006") + ". Events:\n" + string(eventsJSON)
	instruction := explainInstruction
	if cfg.Context != "" {
		instruction += "\n\nAbout them: " + cfg.Context
	}

	body, _ := json.Marshal(map[string]any{
		"model":              cfg.Model,
		"input":              input,
		"system_instruction": instruction,
		"generation_config":  map[string]any{"thinking_level": "low", "max_output_tokens": 4000},
		// Search finds what's actually on: fixtures, films, gigs.
		"tools": []any{map[string]any{"type": "google_search"}},
		"response_format": map[string]any{
			"type":      "text",
			"mime_type": "application/json",
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"notes": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id":   map[string]any{"type": "integer"},
								"note": map[string]any{"type": "string"},
							},
							"required": []string{"id", "note"},
						},
					},
				},
				"required": []string{"notes"},
			},
		},
		"store": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", cfg.APIKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("gemini returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
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
		return nil, fmt.Errorf("decoding gemini's reply: %w", err)
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
	var answer struct {
		Notes []struct {
			ID   int    `json:"id"`
			Note string `json:"note"`
		} `json:"notes"`
	}
	if err := json.Unmarshal([]byte(text.String()), &answer); err != nil {
		return nil, fmt.Errorf("gemini's reply isn't the JSON asked for: %w", err)
	}
	out := map[int]string{}
	for _, n := range answer.Notes {
		if n.ID >= 0 && n.ID < len(events) {
			out[n.ID] = strings.TrimSuffix(strings.TrimSpace(n.Note), ".")
		}
	}
	return out, nil
}

// clip shortens s to at most n characters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
