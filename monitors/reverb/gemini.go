package reverb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Gemini checks each listing against what's wanted, and names it.
type Gemini struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.8-flash: telling one version of a model
	// from another needs what it knows of them, which the lite model is
	// weaker on.
	Model string `json:"model"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

type gemini struct{ Gemini }

func newGemini(c Gemini) *gemini {
	if c.Model == "" {
		c.Model = "gemini-3.8-flash"
	}
	if c.URL == "" {
		c.URL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	}
	return &gemini{c}
}

const instruction = `You check Reverb listings for someone's personal dashboard against what they're looking for, given below.

A listing matches only if it's one of the instruments wanted, whole: not a part, body, neck, case, pickup set, accessory or catalogue for one, and not a different model or version, even one with a similar name or from the same series. Sellers' titles are padded and often leave out model names, so go by everything given: title, make, model, year, finish, category and description, and what you know of the models. If a listing is a wanted model but you can't tell whether it's a wanted version, count it as a match and say what's unclear in the note.

For each listing give:
- match: true or false.
- name: the instrument as it's properly called, short, with its finish, e.g. "Ibanez XPT700 Xiphos, Red Chameleon"; for something else, what it is.
- note: a few words a buyer would want that the name doesn't say. For a match, the version or year when the listing gives it or it follows for certain from what it gives, or what's unclear, e.g. "Mk II", "2017", "7-string" or "mark not stated"; never guess a year or call it something the listing doesn't support. Leave it empty when there's nothing to add. For anything else, why not, e.g. "Mk III" or "body only".`

var schema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    map[string]any{"type": "string"},
			"match": map[string]any{"type": "boolean"},
			"name":  map[string]any{"type": "string"},
			"note":  map[string]any{"type": "string"},
		},
		"required": []string{"id", "match", "name", "note"},
	},
}

// geminiTimeout allows for a slow reply checking a batch of listings.
const geminiTimeout = 90 * time.Second

// geminiBatch is how many listings are asked about at once.
const geminiBatch = 40

// descriptionChars is how much of a listing's description Gemini sees: enough
// for the specs that tell versions apart.
const descriptionChars = 600

// verdict is what Gemini said about a listing.
type verdict struct {
	Match bool   `json:"match"`
	Name  string `json:"name"`
	Note  string `json:"note,omitempty"`
}

var tags = regexp.MustCompile(`<[^>]*>`)

// plain is a listing's HTML description as plain text, cut short.
func plain(s string) string {
	s = strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(s, " "))), " ")
	if r := []rune(s); len(r) > descriptionChars {
		s = string(r[:descriptionChars]) + "…"
	}
	return s
}

// judge asks Gemini whether each listing is what's wanted, by ID.
func (g *gemini) judge(ctx context.Context, client *http.Client, want string, ls []listing) (map[string]verdict, error) {
	type in struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Make        string `json:"make,omitempty"`
		Model       string `json:"model,omitempty"`
		Year        string `json:"year,omitempty"`
		Finish      string `json:"finish,omitempty"`
		Category    string `json:"category,omitempty"`
		Description string `json:"description,omitempty"`
	}
	var list []in
	for _, l := range ls {
		var cats []string
		for _, c := range l.Categories {
			cats = append(cats, c.FullName)
		}
		list = append(list, in{l.id(), l.Title, l.Make, l.Model, l.Year, l.Finish, strings.Join(cats, "; "), plain(l.Description)})
	}
	input, _ := json.Marshal(list)
	body, _ := json.Marshal(map[string]any{
		"model":              g.Model,
		"input":              "Check each listing, by its id:\n" + string(input),
		"system_instruction": instruction + "\n\nWhat they're looking for:\n" + want,
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
	var got []struct {
		ID, Name, Note string
		Match          bool
	}
	if err := json.Unmarshal([]byte(text.String()), &got); err != nil {
		return nil, fmt.Errorf("Gemini's reply isn't the JSON asked for: %w", err)
	}
	asked := map[string]bool{}
	for _, l := range ls {
		asked[l.id()] = true
	}
	verdicts := map[string]verdict{}
	for _, v := range got {
		if n := strings.TrimSpace(v.Name); n != "" && asked[v.ID] {
			verdicts[v.ID] = verdict{Match: v.Match, Name: n, Note: strings.TrimSpace(v.Note)}
		}
	}
	return verdicts, nil
}
