// Package ai asks a language model for JSON on behalf of a monitor, through
// Gemini or Claude, whichever is chosen for that monitor.
//
// Each monitor that uses a model makes a Client for its Use (calendar notes,
// reading mail, and so on). Which provider and model a Use goes to is kept
// in Settings, changed from clients through the "ai" monitor, so a model can
// be swapped without a deploy.
package ai

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Provider names who runs the model.
type Provider string

const (
	Gemini Provider = "gemini"
	Claude Provider = "claude"
)

// Name is the provider as people call it, e.g. for errors.
func (p Provider) Name() string {
	switch p {
	case Claude:
		return "Claude"
	default:
		return "Gemini"
	}
}

// ErrUnavailable wraps an error that isn't about what was asked: the key's
// refused, out of credit or rate-limited, so every request would fail.
var ErrUnavailable = errors.New("AI unavailable")

// File is something sent with the text, e.g. a receipt's PDF.
type File struct {
	// Type is the MIME type, e.g. "application/pdf".
	Type string
	Data []byte
}

// Request is one question.
type Request struct {
	// System is the instruction: what the model is for and how to answer.
	System string
	// Text is what's asked about.
	Text string
	// Files go before Text.
	Files []File
	// Schema is the JSON schema the reply must follow.
	Schema map[string]any
	// MaxTokens caps the reply, thinking included.
	MaxTokens int
	// Search lets the model search the web, e.g. for what's on.
	Search bool
}

// Client asks the model chosen for one Use.
type Client struct {
	use      Use
	gemini   GeminiConfig
	claude   ClaudeConfig
	settings *Settings
	timeout  time.Duration
}

// GeminiConfig is how to reach Gemini.
type GeminiConfig struct {
	APIKey string
	// URL replaces the Interactions API endpoint, for tests.
	URL string
}

// ClaudeConfig is how to reach Claude.
type ClaudeConfig struct {
	// APIKey defaults to the ANTHROPIC_API_KEY environment variable.
	APIKey string
	// URL replaces the API's base URL, for tests.
	URL string
}

// Option changes a Client.
type Option func(*Client)

// WithClaude sets how to reach Claude.
func WithClaude(c ClaudeConfig) Option { return func(cl *Client) { cl.claude = c } }

// WithSettings uses s rather than Shared, for tests.
func WithSettings(s *Settings) Option { return func(cl *Client) { cl.settings = s } }

// WithTimeout bounds each request; 90 seconds by default.
func WithTimeout(d time.Duration) Option { return func(cl *Client) { cl.timeout = d } }

// New makes a Client for u and registers u in its Settings, so clients can
// choose its model.
func New(u Use, g GeminiConfig, opts ...Option) *Client {
	c := &Client{use: u, gemini: g, settings: Shared, timeout: 90 * time.Second}
	for _, o := range opts {
		o(c)
	}
	if c.gemini.URL == "" {
		c.gemini.URL = defaultGeminiURL
	}
	c.settings.Register(u)
	return c
}

// Provider is who the Use goes to at the moment.
func (c *Client) Provider() Provider { return c.settings.Choice(c.use.ID).Provider }

// Ask sends r to the chosen model and decodes its JSON reply into out.
// Errors are prefixed with the provider's name.
func (c *Client) Ask(ctx context.Context, client *http.Client, r Request, out any) error {
	ch := c.settings.Choice(c.use.ID)
	slow := *client
	slow.Timeout = c.timeout
	var err error
	switch ch.Provider {
	case Claude:
		err = askClaude(ctx, &slow, c.claude, ch.Claude, r, out)
	default:
		err = askGemini(ctx, &slow, c.gemini, ch.Gemini, r, out)
	}
	if err != nil {
		return &providerError{p: ch.Provider, err: err}
	}
	return nil
}

// providerError says which provider failed.
type providerError struct {
	p   Provider
	err error
}

func (e *providerError) Error() string { return e.p.Name() + ": " + e.err.Error() }
func (e *providerError) Unwrap() error { return e.err }
