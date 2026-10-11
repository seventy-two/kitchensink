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
			notes = append(notes, map[string]any{"id": e.ID, "note": "about " + e.Title + "."})
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
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"All day | Bins out | Personal",
		" | about Bins out | ",
		"09:00–09:15 | Standup | Work",
		" | about Standup | ",
		"15:00–16:00 | Quarterly review · Room 4 | Work",
		" | about Quarterly review | ",
	}, rows(res.Blocks[0]), "a note under each of today's events, its full stop dropped")
	assert.Equal(t, monitor.ToneMuted, res.Blocks[0].Rows[1][1].Tone)
	assert.Equal(t, []string{"10:00–10:15 | Standup (moved) | Work", "22:00–… | Late film | Personal"}, rows(res.Blocks[1]),
		"only today's")

	require.Equal(t, 1, gemini.calls(), "one request for all of them")
	body := gemini.bodies[0]
	assert.Equal(t, "k3y", gemini.keys[0])
	assert.Equal(t, defaultModel, body["model"])
	assert.Equal(t, false, body["store"])
	assert.Contains(t, body["system_instruction"], "About them: Lives in Bristol")
	assert.Contains(t, body["input"], `"title":"Quarterly review","when":"15:00–16:00","location":"Room 4","calendar":"Work"`)
	assert.Contains(t, body["input"], `"when":"all day"`)
	assert.Contains(t, body["input"], "Today is Monday 12 October 2026.")
	assert.Equal(t, []any{map[string]any{"type": "google_search"}}, body["tools"], "searches for what's on")
	assert.NotContains(t, body["input"], "Late film", "tomorrow isn't asked about")
	format := body["response_format"].(map[string]any)
	assert.Equal(t, "application/json", format["mime_type"])

	// Kept for the day: no more requests.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, 1, gemini.calls())

	// The next day, only the new day's events are asked about.
	next := env
	next.Now = func() time.Time { return now.AddDate(0, 0, 1) }
	_, err = m.Poll(context.Background(), next)
	require.NoError(t, err)
	require.Equal(t, 2, gemini.calls())
	assert.Contains(t, gemini.bodies[1]["input"], "Standup (moved)")
	assert.NotContains(t, gemini.bodies[1]["input"], "Bins out")
}

func TestExplainFailing(t *testing.T) {
	work, _ := feeds(t)
	gemini := newFakeGemini(t)
	gemini.fail = true
	m := explainMonitor(t, gemini.URL, work)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err, "the calendar still shows")
	assert.Equal(t, []string{"09:00–09:15 | Standup", "15:00–16:00 | Quarterly review · Room 4"}, rows(res.Blocks[0]))

	// It tries again on the next poll, and the notes appear.
	gemini.mu.Lock()
	gemini.fail = false
	gemini.mu.Unlock()
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks[0].Rows, 4)
	assert.Equal(t, 2, gemini.calls())
}

func TestExplainConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{"calendars": [{"url": "https://example.com"}], "explain": {}}`))
	assert.Error(t, err, "needs a key")
	assert.Equal(t, "short", clip("  short  ", 10))
	assert.Equal(t, "abc…", clip("abcdef", 3))
}
