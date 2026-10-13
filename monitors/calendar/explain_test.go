package calendar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeGemini answers the Interactions API with a note for each event,
// "about <title>", and records what it was asked.
type fakeGemini struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
	fail   bool
}

func newFakeGemini(t *testing.T) *fakeGemini {
	f := &fakeGemini{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.keys = append(f.keys, r.Header.Get("x-goog-api-key"))
		fail := f.fail
		f.mu.Unlock()
		if fail {
			http.Error(w, `{"error":"quota"}`, http.StatusTooManyRequests)
			return
		}
		input := body["input"].(string)
		var events []explainEvent
		json.Unmarshal([]byte(input[strings.Index(input, "["):]), &events)
		var notes []map[string]any
		for _, e := range events {
			text := "about " + e.Title + "."
			if e.Title == "Quarterly review" {
				text = "" // nothing to add
			}
			notes = append(notes, map[string]any{"id": e.ID, "note": text})
		}
		text, _ := json.Marshal(map[string]any{"notes": notes})
		json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"steps": []any{
				map[string]any{"type": "thought", "signature": "x"},
				map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}}},
			},
		})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeGemini) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

func explainMonitor(t *testing.T, gemini string, cals ...Calendar) *Monitor {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"calendars": cals,
		"explain":   map[string]any{"apiKey": "k3y", "context": "Lives in Bristol", "url": gemini},
	})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestExplain(t *testing.T) {
	work, personal := feeds(t)
	gemini := newFakeGemini(t)
	m := explainMonitor(t, gemini.URL, work, personal)
	env := monitortest.Env(now) // Monday 12 October

	details := func(b monitor.Block) []string {
		var out []string
		for _, r := range b.Rows {
			out = append(out, r[1].Text+" -> "+r[1].Detail)
		}
		return out
	}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"All day | Bins out | Personal",
		"09:00–09:15 | Standup | Work",
		"15:00–16:00 | Quarterly review · Room 4 | Work",
	}, rows(res.Blocks[0]), "the titles stay as they were")
	assert.Equal(t, []string{
		"Bins out -> about Bins out",
		"Standup -> about Standup",
		"Quarterly review · Room 4 -> ",
	}, details(res.Blocks[0]), "each note under its title, its full stop dropped")
	assert.Equal(t, []string{
		"Standup (moved) -> about Standup (moved)",
		"Late film -> about Late film",
	}, details(res.Blocks[1]), "later days get notes too")
	assert.Equal(t, "Weekend in Cardiff -> about Weekend in Cardiff", details(res.Blocks[6])[0], "on each day of a multi-day event")
	require.Len(t, res.Summary, 1)
	assert.Equal(t, monitor.Stat{Label: "in 8 min", Value: "Standup", Detail: "about Standup"}, res.Summary[0], "the next event, with its note")

	require.Equal(t, 1, gemini.calls(), "one request for all of them")
	body := gemini.bodies[0]
	assert.Equal(t, "k3y", gemini.keys[0])
	assert.Equal(t, defaultModel, body["model"])
	assert.Equal(t, false, body["store"])
	assert.Contains(t, body["system_instruction"], "About them: Lives in Bristol")
	input := body["input"].(string)
	assert.Contains(t, input, "Today is Monday 12 October 2026.")
	assert.Contains(t, input, `"title":"Quarterly review","when":"Mon 12 Oct, 15:00–16:00","location":"Room 4","calendar":"Work"`)
	assert.Contains(t, input, `"title":"Bins out","when":"Mon 12 Oct, all day"`)
	assert.Contains(t, input, `"title":"Weekend in Cardiff","when":"Fri 16 Oct to Sun 18 Oct"`)
	assert.Equal(t, 1, strings.Count(input, "Weekend in Cardiff"), "a multi-day event is asked about once")
	assert.Equal(t, []any{map[string]any{"type": "google_search"}}, body["tools"], "searches for what's on")
	format := body["response_format"].(map[string]any)
	assert.Equal(t, "application/json", format["mime_type"])

	// Kept, empty ones too: no more requests the same day.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, gemini.calls())

	// The next day, that day's events are asked about again, for the
	// latest details; later ones keep their notes, and past ones go.
	next := env
	next.Now = func() time.Time { return now.AddDate(0, 0, 1) }
	res, err = m.Poll(context.Background(), next)
	require.NoError(t, err)
	require.Equal(t, 2, gemini.calls())
	input = gemini.bodies[1]["input"].(string)
	assert.Contains(t, input, "Standup (moved)")
	assert.Contains(t, input, "Late film")
	assert.NotContains(t, input, "Call with Mum", "Wednesday's note is kept")
	assert.NotContains(t, input, "Bins out")
	assert.Contains(t, details(res.Blocks[1]), "Call with Mum -> about Call with Mum")
	var saved map[string]note
	_, err = next.State.Get(notesKey, &saved)
	require.NoError(t, err)
	for _, n := range saved {
		assert.NotEqual(t, "about Bins out", n.Text, "past events' notes are dropped")
	}
}

func TestExplainFailing(t *testing.T) {
	work, _ := feeds(t)
	gemini := newFakeGemini(t)
	gemini.fail = true
	m := explainMonitor(t, gemini.URL, work)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err, "the calendar still shows")
	assert.Empty(t, res.Blocks[0].Rows[0][1].Detail)
	assert.Equal(t, []string{"09:00–09:15 | Standup", "15:00–16:00 | Quarterly review · Room 4"}, rows(res.Blocks[0]))

	// It tries again on the next poll, and the notes appear.
	gemini.mu.Lock()
	gemini.fail = false
	gemini.mu.Unlock()
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "about Standup", res.Blocks[0].Rows[0][1].Detail)
	assert.Equal(t, 2, gemini.calls())
}

func TestExplainConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{"calendars": [{"url": "https://example.com"}], "explain": {}}`))
	assert.Error(t, err, "needs a key")
	assert.Equal(t, "short", clip("  short  ", 10))
	assert.Equal(t, "abc…", clip("abcdef", 3))
}

func TestExplainContextChanged(t *testing.T) {
	work, personal := feeds(t)
	gemini := newFakeGemini(t)
	m := explainMonitor(t, gemini.URL, work, personal)
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 1, gemini.calls())

	// Telling Gemini more about the user redoes every note straight away,
	// later days' too, rather than waiting for each event's day.
	m.cfg.Explain.Context = "Lives in Bristol. Their local cinema is the Odeon."
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 2, gemini.calls())
	input := gemini.bodies[1]["input"].(string)
	assert.Contains(t, input, "Late film")
	assert.Contains(t, input, "Call with Mum")
	assert.Contains(t, gemini.bodies[1]["system_instruction"], "Their local cinema is the Odeon")
}
