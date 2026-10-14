package ebay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Gemini names what each listing is.
type Gemini struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.5-flash-lite: naming a listing is simple
	// work.
	Model string `json:"model"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

type gemini struct{ Gemini }

func newGemini(c Gemini) *gemini {
	if c.Model == "" {
		c.Model = "gemini-3.5-flash-lite"
	}
	if c.URL == "" {
		c.URL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	}
	return &gemini{c}
}

const instruction = `You name eBay listings for a personal dashboard. Sellers pad their titles with keywords (platform, condition, "rare", "vintage", "big box", "PC CD-ROM", "complete", years, publishers, "VGC", "tested"), so give only what's being sold, as it's properly written: its official title with the right capitals and punctuation, e.g. "Ultima VII: The Black Gate", "Half-Life", "SimCity 2000". Keep an edition or extra only when it's part of what's sold ("Collector's Edition", "with Expansion Pack"). For several items, join them with " + ", or say "N games" for a large lot. If you can't tell what it is, tidy the title down to its likely name.`

var schema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"name": map[string]any{"type": "string"},
		},
		"required": []string{"id", "name"},
	},
}

// geminiTimeout allows for a slow reply naming a page of listings.
const geminiTimeout = 90 * time.Second

// name asks Gemini what each listing is, by item ID. what says what kind of
// thing they are, e.g. "a boxed PC game".
func (g *gemini) name(ctx context.Context, client *http.Client, what string, items []item) (map[string]string, error) {
	type in struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	var list []in
	for _, it := range items {
		list = append(list, in{it.ItemID, it.Title})
	}
	input, _ := json.Marshal(list)
	sys := instruction
	if what != "" {
		sys += "\n\nEach listing is " + what + "."
	}
	body, _ := json.Marshal(map[string]any{
		"model":              g.Model,
		"input":              "Name each listing, by its id:\n" + string(input),
		"system_instruction": sys,
		"generation_config":  map[string]any{"thinking_level": "low", "max_output_tokens": 8000},
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": schema},
		"store":              false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	slow := *client
	slow.Timeout = geminiTimeout
	resp, err := slow.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("Gemini returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out struct {
		Steps []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"steps"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding Gemini's reply: %w", err)
	}
	var text strings.Builder
	for _, s := range out.Steps {
		if s.Type != "model_output" {
			continue
		}
		for _, c := range s.Content {
			if c.Type == "text" {
				text.WriteString(c.Text)
			}
		}
	}
	var got []struct{ ID, Name string }
	if err := json.Unmarshal([]byte(text.String()), &got); err != nil {
		return nil, fmt.Errorf("Gemini's reply isn't the JSON asked for: %w", err)
	}
	names := map[string]string{}
	asked := map[string]bool{}
	for _, it := range items {
		asked[it.ItemID] = true
	}
	for _, g := range got {
		if n := strings.TrimSpace(g.Name); n != "" && asked[g.ID] {
			names[g.ID] = n
		}
	}
	return names, nil
}
