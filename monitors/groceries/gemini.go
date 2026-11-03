package groceries

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Gemini configures the model that reads orders and suggests recipes.
type Gemini struct {
	// APIKey is a Gemini API key; use "${NAME}" to keep it in the
	// environment.
	APIKey string `json:"apiKey"`
	// Model defaults to gemini-3.8-flash: how long food keeps and what
	// to cook with it need what it knows, and it's asked rarely (once an
	// order, and for recipes when what's going off changes).
	Model string `json:"model"`
	// Context tells Gemini about the household, e.g. how many eat and
	// what they don't, for the recipes.
	Context string `json:"context"`
	// URL replaces the Interactions API endpoint, for tests.
	URL string `json:"url"`
}

type gemini struct{ Gemini }

// geminiTimeout bounds each request: reading a long receipt can be slow.
const geminiTimeout = 90 * time.Second

func newGemini(c Gemini) (*gemini, error) {
	if c.APIKey == "" {
		return nil, errors.New("gemini needs an apiKey")
	}
	if c.Model == "" {
		c.Model = "gemini-3.8-flash"
	}
	if c.URL == "" {
		c.URL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	}
	return &gemini{c}, nil
}

const orderInstruction = `You read a supermarket home delivery's receipt for someone's kitchen dashboard and list the food and drink it brought, with how long each keeps, so they can use things before they go off.

The receipt is the email and any attached PDF; the PDF, where there is one, lists everything delivered. Go by what was actually delivered: a substitute replaces what was ordered, and anything marked unavailable or missing didn't come.

- orderRef: the order number, as written.
- delivery: the day it was (or will be) delivered, YYYY-MM-DD.
- items: every food and drink line, once each (combine repeated lines). Leave out everything else: toiletries, medicines, cleaning products, household goods, pet food, flowers, carrier bags, delivery charges.
  - name: short and plain, as they'd say it, keeping what tells it apart from similar things but no brand, size or marketing words: "Mushroom & garlic tortelloni", "Paneer", "Fresh basil", "Semi oat milk", "Vegan tiramisu", "Ploughman's sandwich", "Red onions".
  - qty: how many packs or pieces came; 1 for loose produce sold by weight.
  - storage: where it's kept: "fridge" (anything chilled), "freezer" (anything frozen), "cupboard" or "fruit bowl" (produce kept out of the fridge).
  - keepsDays: how many days after delivery it's still good if kept that way and not opened. Use the use-by or best-before a UK supermarket delivery typically has left on it (they deliver with a few days' life on chilled food), and for fresh produce how long it really lasts at home: e.g. sushi or a sandwich 1, fresh herbs 4, ripe avocado 2, mushrooms 4, soured cream 7, fresh pasta 5, tofu 14, bread 4, hard cheese 30, a whole cucumber 6, lemons 21, onions 30, garlic 60, dried pasta 365, crisps 60, frozen food 90, chocolate 180.`

var orderSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"orderRef": map[string]any{"type": "string"},
		"delivery": map[string]any{"type": "string", "description": "YYYY-MM-DD"},
		"items": map[string]any{"type": "array", "items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string"},
				"qty":       map[string]any{"type": "integer"},
				"storage":   map[string]any{"type": "string", "enum": []string{"fridge", "freezer", "cupboard", "fruit bowl"}},
				"keepsDays": map[string]any{"type": "integer", "description": "days after delivery it's good for, unopened"},
			},
			"required": []string{"name", "qty", "storage", "keepsDays"},
		}},
	},
	"required": []string{"orderRef", "delivery", "items"},
}

// orderReply is what Gemini makes of a receipt.
type orderReply struct {
	OrderRef string `json:"orderRef"`
	Delivery string `json:"delivery"`
	Items    []struct {
		Name      string `json:"name"`
		Qty       int    `json:"qty"`
		Storage   string `json:"storage"`
		KeepsDays int    `json:"keepsDays"`
	} `json:"items"`
}

// orderInput is the receipt as Gemini's given it: its PDFs, then the
// email.
func (m *Monitor) orderInput(o Order) []any {
	var in []any
	for _, f := range o.Files {
		in = append(in, map[string]any{"type": "document", "mime_type": f.Type, "data": f.Data}) // base64 by encoding/json
	}
	text := fmt.Sprintf("Email received %s from %s\nSubject: %s\n\n%s",
		o.Received.In(m.loc).Format("Mon 2 Jan 2006 15:04"), o.From, o.Subject, o.Text)
	return append(in, map[string]any{"type": "text", "text": text})
}

func (g *gemini) order(ctx context.Context, client *http.Client, input []any) (orderReply, error) {
	var x orderReply
	err := g.ask(ctx, client, orderInstruction, input, orderSchema, &x)
	return x, err
}

const recipesInstruction = `You suggest recipes for someone's kitchen dashboard that use up food before it goes off.

You're given what's going off soon, with the day each must be eaten by, and what else is in their kitchen from recent deliveries (with its day too, if it's perishable). Suggest the number of recipes asked for: real, everyday dishes, each a different kind of meal (not three takes on one idea), each using something from the going-off list, and together using up as much of it as possible, then whatever goes off next. Build them from what they have. Assume only basic store-cupboard things beyond that (oil, butter, salt, pepper, common dried herbs and spices, stock cubes, flour, sugar, rice); anything else a recipe needs goes in buy, and the fewer of those the better. Don't count something that's already a meal or snack on its own (a sandwich, sushi, a ready meal, a dessert, crisps) as needing a recipe. Keep to what their shopping suggests they eat: if they buy no meat or fish, use none.

For each:
- title: the dish, as a cookbook would name it, briefly.
- uses: the perishable things it uses from their kitchen, going-off ones first, named as given.
- buy: anything it needs that they don't have and isn't basic, briefly; empty if nothing.
- minutes: about how long it takes, all in.
- method: the recipe in three to five short sentences, with quantities, for the number of people it should serve.`

var recipesSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":   map[string]any{"type": "string"},
			"uses":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"buy":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"minutes": map[string]any{"type": "integer"},
			"method":  map[string]any{"type": "string"},
		},
		"required": []string{"title", "uses", "buy", "minutes", "method"},
	},
}

// Recipe is a suggestion.
type Recipe struct {
	Title   string   `json:"title"`
	Uses    []string `json:"uses"`
	Buy     []string `json:"buy"`
	Minutes int      `json:"minutes"`
	Method  string   `json:"method"`
}

// recipesInput lists what's going off and what else there is.
func (m *Monitor) recipesInput(soon, all []Item, today time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today is %s.\n\nGoing off soon:\n", today.Format("Monday 2 January"))
	in := map[string]bool{}
	for _, it := range soon {
		in[it.ID] = true
		fmt.Fprintf(&b, "- %s (%s): %s\n", it.Name, it.Storage, m.when(it.useBy(m.loc), today))
	}
	b.WriteString("\nAlso in:\n")
	for _, it := range all {
		switch {
		case in[it.ID]:
		case it.useBy(m.loc).After(today.AddDate(0, 0, m.show)):
			fmt.Fprintf(&b, "- %s (%s)\n", it.Name, it.Storage)
		default:
			fmt.Fprintf(&b, "- %s (%s): %s\n", it.Name, it.Storage, m.when(it.useBy(m.loc), today))
		}
	}
	return b.String()
}

func (g *gemini) recipes(ctx context.Context, client *http.Client, input string, n int) ([]Recipe, error) {
	var list []Recipe
	err := g.ask(ctx, client, recipesInstruction, fmt.Sprintf("%s\nSuggest %d recipes.", input, n), recipesSchema, &list)
	if len(list) > n {
		list = list[:n]
	}
	return list, err
}

// errUnavailable is a Gemini error that isn't about what was asked: the
// key's refused, out of credit or rate-limited.
var errUnavailable = errors.New("Gemini unavailable")

// ask sends one request and decodes the JSON reply into out.
func (g *gemini) ask(ctx context.Context, client *http.Client, instruction string, input any, schema map[string]any, out any) error {
	sys := instruction
	if g.Context != "" {
		sys += "\n\nAbout them: " + g.Context
	}
	body, err := json.Marshal(map[string]any{
		"model":              g.Model,
		"input":              input,
		"system_instruction": sys,
		"generation_config":  map[string]any{"thinking_level": "low", "max_output_tokens": 16000},
		"response_format":    map[string]any{"type": "text", "mime_type": "application/json", "schema": schema},
		"store":              false,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.APIKey)
	slow := *client
	slow.Timeout = geminiTimeout
	resp, err := slow.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		err := fmt.Errorf("returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
			return fmt.Errorf("%w: %w", errUnavailable, err)
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
	if err := json.Unmarshal([]byte(text.String()), out); err != nil {
		t := text.String()
		if len(t) > 300 {
			t = t[:300] + "…"
		}
		return fmt.Errorf("the reply isn't the JSON asked for: %w (%q)", err, t)
	}
	return nil
}
