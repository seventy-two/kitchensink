package aimodels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seventy-two/kitchensink/ai"
	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
)

func TestModels(t *testing.T) {
	var geminiCalls, claudeCalls int
	gem := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		geminiCalls++
		w.Write([]byte(`{"models":[
			{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.5-flash-lite","displayName":"Gemini 3.5 Flash Lite","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer gem.Close()
	cla := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claudeCalls++
		assert.Equal(t, "/v1/models", r.URL.Path)
		caps := func(ok bool) map[string]any {
			return map[string]any{"structured_outputs": map[string]any{"supported": true}, "effort": map[string]any{"low": map[string]any{"supported": ok}}}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"has_more": false, "data": []any{
			map[string]any{"id": "claude-haiku-5-5", "display_name": "Claude Haiku 5.5", "lifecycle": "active", "capabilities": caps(true)},
			map[string]any{"id": "claude-sonnet-5-5", "display_name": "Claude Sonnet 5.5", "lifecycle": "active", "capabilities": caps(true)},
			map[string]any{"id": "claude-haiku-4-5", "display_name": "Claude Haiku 4.5", "lifecycle": "active", "capabilities": caps(false)},
		}})
	}))
	defer cla.Close()

	cfg, _ := json.Marshal(map[string]string{"geminiKey": "g", "claudeKey": "c", "geminiURL": gem.URL, "claudeURL": cla.URL})
	m, err := New(cfg)
	require.NoError(t, err)
	m.settings = &ai.Settings{}
	m.settings.Register(ai.Use{ID: "mail", Label: "Mail", Gemini: "gemini-3.5-flash-lite"})
	m.settings.Register(ai.Use{ID: "ebay", Label: "Big box", Gemini: "gemini-old"})

	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	env := monitortest.Env(now)
	env.HTTP = http.DefaultClient
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.True(t, res.Background, "shown with settings")
	rows := res.Blocks[0].Rows
	require.Len(t, rows, 2)

	mail := rows[0]
	assert.Equal(t, "Mail", mail[0].Text)
	assert.Equal(t, "Gemini", mail[1].Text)
	assert.Equal(t, &monitor.Choice{Action: "choose", ID: "mail/provider", Value: "gemini", Hint: "Who Mail asks",
		Options: []monitor.Option{{Label: "Gemini", Value: "gemini"}, {Label: "Claude", Value: "claude"}}}, mail[1].Choice)
	assert.Equal(t, "Gemini 3.5 Flash Lite", mail[2].Text)
	assert.Equal(t, monitor.ToneAccent, mail[2].Tone, "the model in use")
	assert.Len(t, mail[2].Choice.Options, 2)
	assert.Equal(t, "Claude Haiku 5.5", mail[3].Text)
	assert.Equal(t, monitor.ToneMuted, mail[3].Tone)
	assert.Equal(t, []monitor.Option{{Label: "Claude Haiku 5.5", Value: "claude-haiku-5-5"}, {Label: "Claude Sonnet 5.5", Value: "claude-sonnet-5-5"}},
		mail[3].Choice.Options, "no low effort, not offered")
	assert.Equal(t, monitor.Option{Label: "gemini-old", Value: "gemini-old"}, rows[1][2].Choice.Options[0], "a model not listed stays choosable")

	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "choose", ID: "mail/provider", Text: "claude"}))
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "choose", ID: "mail/claude", Text: "claude-sonnet-5-5"}))
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "choose", ID: "mail", Text: "claude"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "choose", ID: "mail/provider", Text: "openai"}), monitor.ErrBadAction)

	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks, 1, "no problems listing models")
	mail = res.Blocks[0].Rows[0]
	assert.Equal(t, "Claude", mail[1].Text)
	assert.Equal(t, "Claude Sonnet 5.5", mail[3].Text)
	assert.Equal(t, monitor.ToneAccent, mail[3].Tone)
	assert.Equal(t, monitor.Stat{Label: "Mail", Value: "Claude Sonnet 5.5"}, res.Summary[0])
	assert.Equal(t, 1, geminiCalls, "lists are cached")
	assert.Equal(t, 1, claudeCalls)
}
