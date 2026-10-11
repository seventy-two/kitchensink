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

// Explain configures notes from Gemini on what each of today's events
// probably is.
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

const explainInstruction = `You annotate entries on someone's calendar for their dashboard. Titles are often their own shorthand or in-jokes. For each event, work out what is actually happening, using what you know about them and Google Search for what's on that day: the actual match, film, gig, show or appointment, and where.

Write one short line (at most 12 words) per event, naming the specifics: teams, film or band, and venue. For example, an event titled "handegg" on the day the Eagles play the Jaguars in London becomes "Going to see the Eagles v Jaguars at Tottenham". Only add "probably" when you can't confirm it. Write in plain UK English, with no full stop at the end.`

// notesVersion is part of each note's key, so changing how notes are
// written replaces the ones already made.
const notesVersion = "2"

// note is Gemini's line about one event, kept for the day it was made.
type note struct {
	Day  string `json:"day"`
	Text string `json:"text"`
}

// noteKey identifies an occurrence for its note. A change of title, time
// or place gets a fresh one.
func noteKey(o occurrence, calName string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{notesVersion, calName, o.UID, o.Start.UTC().Format(time.RFC3339), o.Title, o.Location}, "\x00")))
	return hex.EncodeToString(h[:12])
}

// explain returns notes for today's occurrences, by noteKey. Notes are
// kept for the day, so Gemini is asked only about events it hasn't seen;
// if it fails, those just go without until the next poll.
func (m *Monitor) explain(ctx context.Context, env monitor.Env, today []occurrence, now time.Time) map[string]string {
	day := now.Format("2006-01-02")
	saved := map[string]note{}
	if _, err := env.State.Get(notesKey, &saved); err != nil {
		env.Log.Warn("reading event notes", "err", err)
	}
	out := map[string]string{}
	kept := map[string]note{}
	var ask []occurrence
	var keys []string
	for _, o := range today {
		k := noteKey(o, m.cfg.Calendars[o.Cal].Name)
		if n, ok := saved[k]; ok && n.Day == day {
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
		for i, line := range lines {
			if line == "" {
				continue
			}
			out[keys[i]] = line
			kept[keys[i]] = note{Day: day, Text: line}
		}
	}
	// Saving only today's notes drops yesterday's.
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

// askGemini asks for a line about each event, returned in the same order;
// ones it didn't answer are empty.
func (m *Monitor) askGemini(ctx context.Context, client *http.Client, events []occurrence, now time.Time) ([]string, error) {
	cfg := m.cfg.Explain
	var list []explainEvent
	for i, o := range events {
		when := "all day"
		if !o.AllDay {
			when = o.Start.In(m.loc).Format("15:04") + "–" + o.End.In(m.loc).Format("15:04")
		}
		list = append(list, explainEvent{
			ID: i, Title: o.Title, When: when, Location: o.Location,
			Calendar: m.cfg.Calendars[o.Cal].Name, Description: clip(o.Description, 300),
		})
	}
	eventsJSON, _ := json.Marshal(list)
	input := "Today is " + now.In(m.loc).Format("Monday 2 January 2006") + ". Today's events:\n" + string(eventsJSON)
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
	out := make([]string, len(events))
	for _, n := range answer.Notes {
		if n.ID >= 0 && n.ID < len(out) {
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
