package ai

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestLiveClaude asks the real Claude API a calendar-style question, with
// web search and a list for a reply; it runs only with AI_LIVE_CLAUDE set
// (and ANTHROPIC_API_KEY). AI_LIVE_MODEL picks the model.
func TestLiveClaude(t *testing.T) {
	if os.Getenv("AI_LIVE_CLAUDE") == "" {
		t.Skip("AI_LIVE_CLAUDE not set")
	}
	s := &Settings{}
	c := New(Use{ID: "live"}, GeminiConfig{}, WithSettings(s))
	require.NoError(t, s.Set("live", "provider", "claude"))
	if m := os.Getenv("AI_LIVE_MODEL"); m != "" {
		require.NoError(t, s.Set("live", "claude", m))
	}
	var got []struct {
		ID   int    `json:"id"`
		Note string `json:"note"`
	}
	err := c.Ask(context.Background(), http.DefaultClient, Request{
		System:    `Annotate calendar entries as a short listing: what, when, where, separated by " · ". At most 15 words.`,
		Text:      `Today is Wednesday 18 November 2026. Events: [{"id":0,"title":"watershed","when":"Wed 18 Nov, 20:00-22:00"}]`,
		MaxTokens: 4000,
		Search:    true,
		Schema: map[string]any{"type": "array", "items": map[string]any{"type": "object",
			"properties": map[string]any{"id": map[string]any{"type": "integer"}, "note": map[string]any{"type": "string"}},
			"required":   []string{"id", "note"}}},
	}, &got)
	require.NoError(t, err)
	require.NotEmpty(t, got)
	t.Logf("%+v", got)
}
