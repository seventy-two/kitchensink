package ai

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
)

// maxSearches caps web searches per request: each is billed.
const maxSearches = 5

// claudeClient builds an SDK client for cfg.
func claudeClient(client *http.Client, cfg ClaudeConfig) (anthropic.Client, bool) {
	key := cfg.APIKey
	if key == "" {
		key = os.Getenv("ANTHROPIC_API_KEY")
	}
	if key == "" {
		return anthropic.Client{}, false
	}
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithHTTPClient(client), option.WithMaxRetries(1)}
	if cfg.URL != "" {
		opts = append(opts, option.WithBaseURL(cfg.URL))
	}
	return anthropic.NewClient(opts...), true
}

// askClaude sends r to Claude's Messages API.
func askClaude(ctx context.Context, client *http.Client, cfg ClaudeConfig, model string, r Request, out any) error {
	c, ok := claudeClient(client, cfg)
	if !ok {
		return fmt.Errorf("no API key: %w", ErrUnavailable)
	}
	var blocks []anthropic.ContentBlockParamUnion
	for _, f := range r.Files {
		data := base64.StdEncoding.EncodeToString(f.Data)
		switch {
		case f.Type == "application/pdf":
			blocks = append(blocks, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: data}))
		case strings.HasPrefix(f.Type, "image/"):
			blocks = append(blocks, anthropic.NewImageBlockBase64(f.Type, data))
		case strings.HasPrefix(f.Type, "text/"):
			blocks = append(blocks, anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{Data: string(f.Data)}))
		default:
			return fmt.Errorf("can't send a %s file", f.Type)
		}
	}
	blocks = append(blocks, anthropic.NewTextBlock(r.Text))
	schema, wrapped := strictSchema(r.Schema)
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(r.MaxTokens),
		System:    []anthropic.TextBlockParam{{Text: r.System}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: anthropic.OutputConfigEffortLow,
			Format: anthropic.JSONOutputFormatParam{Schema: schema},
		},
	}
	if r.Search {
		params.Tools = []anthropic.ToolUnionParam{{OfWebSearchTool20250305: &anthropic.WebSearchTool20250305Param{
			MaxUses: anthropic.Int(maxSearches),
		}}}
	}

	var text strings.Builder
	// A turn with web searches can pause; it's carried on by sending the
	// reply so far back.
	for range 4 {
		msg, err := c.Messages.New(ctx, params)
		if err != nil {
			return claudeError(err)
		}
		switch msg.StopReason {
		case anthropic.StopReasonRefusal:
			return fmt.Errorf("declined to answer (%s)", cmpOr(string(msg.StopDetails.Category), "no reason given"))
		case anthropic.StopReasonMaxTokens:
			return fmt.Errorf("the reply was cut off at %d tokens", r.MaxTokens)
		}
		for _, b := range msg.Content {
			if t, ok := b.AsAny().(anthropic.TextBlock); ok {
				text.WriteString(t.Text)
			}
		}
		if msg.StopReason != anthropic.StopReasonPauseTurn {
			break
		}
		text.Reset()
		params.Messages = append(params.Messages, msg.ToParam())
	}
	if !wrapped {
		return decodeReply(text.String(), out)
	}
	var w struct{ Items rawJSON }
	if err := decodeReply(text.String(), &w); err != nil {
		return err
	}
	return decodeReply(string(w.Items), out)
}

// rawJSON keeps a JSON value undecoded.
type rawJSON []byte

func (r *rawJSON) UnmarshalJSON(b []byte) error {
	*r = append((*r)[:0], b...)
	return nil
}

// claudeError marks errors that every request would get.
func claudeError(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		// Running out of credit is a 400 with this message.
		if unavailableStatus(apierr.StatusCode) || strings.Contains(strings.ToLower(apierr.Error()), "credit balance") {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return err
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// strictSchema adapts a schema written for Gemini to Claude's structured
// outputs: every object closed to other properties, and the reply an
// object, so a top-level array goes in an "items" property (wrapped says
// so). The original isn't changed.
func strictSchema(s map[string]any) (out map[string]any, wrapped bool) {
	out, _ = closeObjects(s).(map[string]any)
	if out["type"] != "object" {
		return map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"items": out},
			"required":             []string{"items"},
			"additionalProperties": false,
		}, true
	}
	return out, false
}

// closeObjects copies v, adding additionalProperties false to each object
// schema and dropping constraints Claude doesn't take.
func closeObjects(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x)+1)
		for k, val := range x {
			switch k {
			case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
				"minLength", "maxLength", "minItems", "maxItems", "pattern":
				continue
			}
			m[k] = closeObjects(val)
		}
		if m["type"] == "object" {
			m["additionalProperties"] = false
		}
		return m
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = closeObjects(val)
		}
		return out
	default:
		return v
	}
}
