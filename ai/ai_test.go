package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memState is a State in memory.
type memState map[string][]byte

func (m memState) Get(key string, v any) (bool, error) {
	b, ok := m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, v)
}

func (m memState) Set(key string, v any) error {
	b, err := json.Marshal(v)
	m[key] = b
	return err
}

var listSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string"}, "n": map[string]any{"type": "integer", "minimum": 0}},
		"required":   []string{"id", "n"},
	},
}

// fakeClaude answers Messages requests with reply, keeping each body.
func fakeClaude(t *testing.T, status int, reply string) (*httptest.Server, *[]map[string]any) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/messages", r.URL.Path)
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		var b map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&b))
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func message(text, stop string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-haiku-5-5",
		"content":     []any{map[string]any{"type": "thinking", "thinking": "", "signature": "x"}, map[string]any{"type": "text", "text": text}},
		"stop_reason": stop, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
	return string(b)
}

func TestClaude(t *testing.T) {
	srv, bodies := fakeClaude(t, http.StatusOK, message(`{"items":[{"id":"a","n":2}]}`, "end_turn"))
	s := &Settings{}
	c := New(Use{ID: "x", Label: "X", Gemini: "gemini-3.8-flash"}, GeminiConfig{APIKey: "g"},
		WithSettings(s), WithClaude(ClaudeConfig{APIKey: "test-key", URL: srv.URL}))
	require.NoError(t, s.Set("x", "provider", "claude"))
	require.NoError(t, s.Set("x", "claude", "claude-sonnet-5-5"))

	var got []struct {
		ID string
		N  int
	}
	err := c.Ask(context.Background(), http.DefaultClient, Request{
		System: "sys", Text: "hello", Schema: listSchema, MaxTokens: 500, Search: true,
		Files: []File{{Type: "application/pdf", Data: []byte("%PDF")}},
	}, &got)
	require.NoError(t, err)
	assert.Equal(t, []struct {
		ID string
		N  int
	}{{"a", 2}}, got, "unwrapped from items")

	require.Len(t, *bodies, 1)
	b := (*bodies)[0]
	assert.Equal(t, "claude-sonnet-5-5", b["model"])
	assert.EqualValues(t, 500, b["max_tokens"])
	assert.Equal(t, []any{map[string]any{"type": "text", "text": "sys"}}, b["system"])
	content := b["messages"].([]any)[0].(map[string]any)["content"].([]any)
	assert.Equal(t, "document", content[0].(map[string]any)["type"], "the PDF goes first")
	assert.Equal(t, map[string]any{"type": "text", "text": "hello"}, content[1])
	oc := b["output_config"].(map[string]any)
	assert.Equal(t, "low", oc["effort"])
	schema := oc["format"].(map[string]any)["schema"].(map[string]any)
	assert.Equal(t, "object", schema["type"], "a top-level array is wrapped")
	assert.Equal(t, false, schema["additionalProperties"])
	item := schema["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	assert.Equal(t, false, item["additionalProperties"], "inner objects are closed")
	assert.NotContains(t, item["properties"].(map[string]any)["n"], "minimum", "unsupported constraints dropped")
	tools := b["tools"].([]any)
	assert.Equal(t, "web_search_20250305", tools[0].(map[string]any)["type"])
	assert.EqualValues(t, maxSearches, tools[0].(map[string]any)["max_uses"])
	_, hasMin := listSchema["items"].(map[string]any)["properties"].(map[string]any)["n"].(map[string]any)["minimum"]
	assert.True(t, hasMin, "the caller's schema isn't changed")
}

func TestClaudeErrors(t *testing.T) {
	ask := func(status int, reply string) error {
		srv, _ := fakeClaude(t, status, reply)
		s := &Settings{}
		c := New(Use{ID: "x"}, GeminiConfig{}, WithSettings(s), WithClaude(ClaudeConfig{APIKey: "test-key", URL: srv.URL}))
		require.NoError(t, s.Set("x", "provider", "claude"))
		var out map[string]any
		return c.Ask(context.Background(), http.DefaultClient, Request{Text: "hi", Schema: map[string]any{"type": "object"}, MaxTokens: 10}, &out)
	}
	err := ask(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low to access the Anthropic API."}}`)
	assert.ErrorIs(t, err, ErrUnavailable, "out of credit")
	assert.ErrorContains(t, err, "Claude: ")
	assert.ErrorIs(t, ask(http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`), ErrUnavailable)
	err = ask(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"bad schema"}}`)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrUnavailable), "about this request only")
	assert.ErrorContains(t, ask(http.StatusOK, message("", "refusal")), "declined")
	assert.ErrorContains(t, ask(http.StatusOK, message(`{"a":`, "max_tokens")), "cut off")
}

func TestGemini(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "g-key", r.Header.Get("x-goog-api-key"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Write([]byte(`{"steps":[{"type":"thought"},{"type":"model_output","content":[{"type":"text","text":"[{\"id\":\"a\",\"n\":1}]"}]}]}`))
	}))
	defer srv.Close()
	s := &Settings{}
	c := New(Use{ID: "x", Gemini: "gemini-3.8-flash"}, GeminiConfig{APIKey: "g-key", URL: srv.URL}, WithSettings(s))
	var got []map[string]any
	require.NoError(t, c.Ask(context.Background(), http.DefaultClient, Request{System: "sys", Text: "hi", Schema: listSchema, MaxTokens: 50, Search: true}, &got))
	assert.Equal(t, []map[string]any{{"id": "a", "n": float64(1)}}, got)
	assert.Equal(t, "gemini-3.8-flash", body["model"])
	assert.Equal(t, "hi", body["input"])
	assert.Equal(t, "sys", body["system_instruction"])
	assert.Equal(t, []any{map[string]any{"type": "google_search"}}, body["tools"])
	assert.Equal(t, listSchema["type"], body["response_format"].(map[string]any)["schema"].(map[string]any)["type"], "Gemini takes arrays as they are")

	require.NoError(t, s.Set("x", "gemini", "gemini-3.5-flash-lite"))
	require.NoError(t, c.Ask(context.Background(), http.DefaultClient, Request{Text: "hi", Schema: listSchema}, &got))
	assert.Equal(t, "gemini-3.5-flash-lite", body["model"], "the chosen model")
}

func TestSettings(t *testing.T) {
	st := memState{}
	s := &Settings{}
	s.Register(Use{ID: "mail", Label: "Mail", Gemini: "gemini-3.5-flash-lite"})
	require.NoError(t, s.Attach(st))
	assert.Equal(t, Choice{Provider: Gemini, Gemini: "gemini-3.5-flash-lite", Claude: DefaultClaudeModel}, s.Choice("mail"), "defaults")

	require.NoError(t, s.Set("mail", "provider", "claude"))
	require.NoError(t, s.Set("mail", "claude", "claude-sonnet-5-5"))
	assert.Error(t, s.Set("mail", "provider", "openai"))
	assert.Error(t, s.Set("nope", "provider", "claude"))
	assert.Error(t, s.Set("mail", "colour", "red"))
	assert.Error(t, s.Set("mail", "gemini", ""))

	// A restart: saved choices come back.
	s2 := &Settings{}
	s2.Register(Use{ID: "mail", Label: "Mail", Gemini: "gemini-3.5-flash-lite"})
	require.NoError(t, s2.Attach(st))
	assert.Equal(t, Choice{Provider: Claude, Gemini: "gemini-3.5-flash-lite", Claude: "claude-sonnet-5-5"}, s2.Choice("mail"))
}

func TestGeminiModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[
			{"name":"models/gemini-2.5-flash","displayName":"Gemini 2.5 Flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-flash-latest","displayName":"Gemini Flash Latest","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.5-flash-lite","displayName":"Gemini 3.5 Flash Lite","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-flash-tts","displayName":"TTS","supportedGenerationMethods":["generateContent"]},
			{"name":"models/gemini-3.8-live","displayName":"Live","supportedGenerationMethods":["bidiGenerateContent"]},
			{"name":"models/gemma-4-31b-it","displayName":"Gemma","supportedGenerationMethods":["generateContent"]}]}`))
	}))
	defer srv.Close()
	got, err := GeminiModels(context.Background(), http.DefaultClient, "k", srv.URL)
	require.NoError(t, err)
	assert.Equal(t, []Model{
		{ID: "gemini-3.8-flash", Name: "Gemini 3.8 Flash"},
		{ID: "gemini-3.5-flash-lite", Name: "Gemini 3.5 Flash Lite"},
		{ID: "gemini-flash-latest", Name: "Gemini Flash Latest"},
	}, got)
}
