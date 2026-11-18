package ai

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
)

// Model is one model offered for a Use.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

const defaultGeminiModelsURL = "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1000"

// notText marks Gemini models that aren't for answering in text: speech,
// images, music, live audio and the like.
var notText = []string{"tts", "image", "embedding", "live", "audio", "transcribe", "robotics",
	"computer-use", "customtools", "lyria", "banana", "omni", "deep-research", "antigravity"}

// GeminiModels lists the Gemini models that can answer the monitors: text
// models from Gemini 3 on (older ones don't take a thinking level), newest
// first, with the "-latest" aliases last. url replaces the endpoint, for
// tests.
func GeminiModels(ctx context.Context, client *http.Client, key, url string) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cmp.Or(url, defaultGeminiModelsURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", key)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("Gemini returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var in struct {
		Models []struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Methods     []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&in); err != nil {
		return nil, err
	}
	var out []Model
	for _, m := range in.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if !strings.HasPrefix(id, "gemini-") || strings.HasPrefix(id, "gemini-2") || !slices.Contains(m.Methods, "generateContent") ||
			slices.ContainsFunc(notText, func(w string) bool { return strings.Contains(id, w) }) {
			continue
		}
		out = append(out, Model{ID: id, Name: cmp.Or(m.DisplayName, id)})
	}
	slices.SortFunc(out, func(a, b Model) int {
		al, bl := strings.HasSuffix(a.ID, "-latest"), strings.HasSuffix(b.ID, "-latest")
		if al != bl {
			if al {
				return 1
			}
			return -1
		}
		return strings.Compare(b.ID, a.ID)
	})
	return out, nil
}

// ClaudeModels lists the Claude models that can answer the monitors:
// current ones that take structured outputs and low effort, in the API's
// order (newest first).
func ClaudeModels(ctx context.Context, client *http.Client, cfg ClaudeConfig) ([]Model, error) {
	c, ok := claudeClient(client, cfg)
	if !ok {
		return nil, fmt.Errorf("Claude has no API key")
	}
	var out []Model
	pager := c.Models.ListAutoPaging(ctx, anthropic.ModelListParams{Limit: anthropic.Int(100)})
	for pager.Next() {
		m := pager.Current()
		if m.Lifecycle == anthropic.ModelInfoLifecycleRetired || m.Lifecycle == anthropic.ModelInfoLifecycleDeprecated ||
			!m.Capabilities.StructuredOutputs.Supported || !m.Capabilities.Effort.Low.Supported {
			continue
		}
		out = append(out, Model{ID: m.ID, Name: cmp.Or(m.DisplayName, m.ID)})
	}
	if err := pager.Err(); err != nil {
		return nil, fmt.Errorf("Claude: %w", err)
	}
	return out, nil
}
