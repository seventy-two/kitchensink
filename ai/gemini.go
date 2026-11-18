package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const defaultGeminiURL = "https://generativelanguage.googleapis.com/v1beta/interactions"

// askGemini sends r to Gemini's Interactions API.
func askGemini(ctx context.Context, client *http.Client, cfg GeminiConfig, model string, r Request, out any) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("no API key: %w", ErrUnavailable)
	}
	var input any = r.Text
	if len(r.Files) > 0 {
		var parts []any
		for _, f := range r.Files {
			parts = append(parts, map[string]any{"type": "document", "mime_type": f.Type, "data": f.Data}) // base64 by encoding/json
		}
		input = append(parts, map[string]any{"type": "text", "text": r.Text})
	}
	req := map[string]any{
		"model":              model,
		"input":              input,
		"system_instruction": r.System,
		"generation_config":  map[string]any{"thinking_level": "low", "max_output_tokens": r.MaxTokens},
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": r.Schema},
		"store":              false,
	}
	if r.Search {
		req["tools"] = []any{map[string]any{"type": "google_search"}}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("x-goog-api-key", cfg.APIKey)
	resp, err := client.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		err := fmt.Errorf("returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		if unavailableStatus(resp.StatusCode) {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return err
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
		return fmt.Errorf("decoding the reply: %w", err)
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
	return decodeReply(text.String(), out)
}

// unavailableStatus is an HTTP status that means no request will work for
// now: a refused key, no credit, or a rate limit.
func unavailableStatus(code int) bool {
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
		return true
	}
	return false
}

// decodeReply decodes the model's JSON text into out.
func decodeReply(text string, out any) error {
	if err := json.Unmarshal([]byte(text), out); err != nil {
		if len(text) > 300 {
			text = text[:300] + "…"
		}
		return fmt.Errorf("the reply isn't the JSON asked for: %w (%q)", err, text)
	}
	return nil
}
