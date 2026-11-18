// Package aimodels lets clients choose, for each monitor that asks a
// language model, whether it goes to Gemini or Claude and which model each
// uses. It's a background monitor, so clients show it with their settings.
package aimodels

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/ai"
	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("ai", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// GeminiKey lists Gemini's models; use "${NAME}" to keep it in the
	// environment.
	GeminiKey string `json:"geminiKey"`
	// ClaudeKey lists Claude's; ANTHROPIC_API_KEY by default.
	ClaudeKey string `json:"claudeKey"`
	// Refresh is how often the model lists are fetched again; 12 hours by
	// default.
	Refresh monitor.Duration `json:"refresh"`
	// GeminiURL and ClaudeURL replace the endpoints, for tests.
	GeminiURL string `json:"geminiURL"`
	ClaudeURL string `json:"claudeURL"`
}

// Monitor shows and changes ai.Settings.
type Monitor struct {
	cfg      Config
	settings *ai.Settings
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Refresh: monitor.Duration(12 * time.Hour)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	return &Monitor{cfg: c, settings: ai.Shared}, nil
}

// models is the cached model lists.
type models struct {
	Fetched time.Time  `json:"fetched"`
	Gemini  []ai.Model `json:"gemini"`
	Claude  []ai.Model `json:"claude"`
}

// chooseAction is the Choice action: ID is "<use>/<provider|gemini|claude>",
// Text the value.
const chooseAction = "choose"

// Act changes a choice.
func (m *Monitor) Act(_ context.Context, _ monitor.Env, a monitor.Action) error {
	if a.Type != chooseAction {
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	use, what, ok := strings.Cut(a.ID, "/")
	if !ok {
		return fmt.Errorf("%w: id %q isn't use/setting", monitor.ErrBadAction, a.ID)
	}
	if err := m.settings.Set(use, what, a.Text); err != nil {
		return fmt.Errorf("%w: %w", monitor.ErrBadAction, err)
	}
	return nil
}

// Poll fetches the model lists when they're due and shows the choices.
func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	var ms models
	if _, err := env.State.Get("models", &ms); err != nil {
		return monitor.Result{}, err
	}
	var problems []string
	if env.Now().Sub(ms.Fetched) >= time.Duration(m.cfg.Refresh) || len(ms.Gemini) == 0 || len(ms.Claude) == 0 {
		fresh := models{Fetched: env.Now(), Gemini: ms.Gemini, Claude: ms.Claude}
		if m.cfg.GeminiKey != "" {
			if g, err := ai.GeminiModels(ctx, env.HTTP, m.cfg.GeminiKey, m.cfg.GeminiURL); err != nil {
				problems = append(problems, "Couldn't list Gemini's models: "+err.Error())
			} else {
				fresh.Gemini = g
			}
		}
		if c, err := ai.ClaudeModels(ctx, env.HTTP, ai.ClaudeConfig{APIKey: m.cfg.ClaudeKey, URL: m.cfg.ClaudeURL}); err != nil {
			problems = append(problems, "Couldn't list Claude's models: "+err.Error())
		} else {
			fresh.Claude = c
		}
		if len(problems) > 0 {
			// Try again in an hour rather than waiting the full refresh.
			fresh.Fetched = env.Now().Add(time.Hour - time.Duration(m.cfg.Refresh))
		}
		ms = fresh
		if err := env.State.Set("models", ms); err != nil {
			return monitor.Result{}, err
		}
	}
	return m.result(ms, problems), nil
}

var providers = []monitor.Option{{Label: "Gemini", Value: string(ai.Gemini)}, {Label: "Claude", Value: string(ai.Claude)}}

func (m *Monitor) result(ms models, problems []string) monitor.Result {
	table := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Monitor"}, {Title: "Uses"}, {Title: "Gemini model", Fill: true}, {Title: "Claude model", Fill: true},
		},
	}
	var summary []monitor.Stat
	for _, u := range m.settings.Uses() {
		c := m.settings.Choice(u.ID)
		gem := modelCell(u.ID+"/gemini", c.Gemini, ms.Gemini, "Gemini model for "+u.Label)
		cla := modelCell(u.ID+"/claude", c.Claude, ms.Claude, "Claude model for "+u.Label)
		using := gem
		if c.Provider == ai.Claude {
			using, cla.Tone, gem.Tone = cla, monitor.ToneAccent, monitor.ToneMuted
		} else {
			gem.Tone, cla.Tone = monitor.ToneAccent, monitor.ToneMuted
		}
		table.Rows = append(table.Rows, []monitor.Cell{
			{Text: u.Label},
			{Text: c.Provider.Name(), Choice: &monitor.Choice{
				Action: chooseAction, ID: u.ID + "/provider", Value: string(c.Provider), Options: providers,
				Hint: "Who " + u.Label + " asks",
			}},
			gem, cla,
		})
		summary = append(summary, monitor.Stat{Label: u.Label, Value: using.Text})
	}
	blocks := []monitor.Block{table}
	if len(problems) > 0 {
		blocks = append(blocks, monitor.Block{Type: monitor.BlockText, Text: strings.Join(problems, "\n")})
	}
	if len(m.settings.Uses()) == 0 {
		blocks = []monitor.Block{{Type: monitor.BlockText, Text: "No monitors ask a model."}}
	}
	return monitor.Result{Blocks: blocks, Summary: summary, SummaryStyle: monitor.SummaryChips, Background: true}
}

// modelCell is a drop-down of models, with the chosen one kept as an option
// even if the list doesn't have it (say it's gone, or the list failed).
func modelCell(id, chosen string, list []ai.Model, hint string) monitor.Cell {
	label := chosen
	var opts []monitor.Option
	for _, md := range list {
		opts = append(opts, monitor.Option{Label: md.Name, Value: md.ID})
		if md.ID == chosen {
			label = md.Name
		}
	}
	if !slices.ContainsFunc(list, func(md ai.Model) bool { return md.ID == chosen }) {
		opts = append([]monitor.Option{{Label: chosen, Value: chosen}}, opts...)
	}
	return monitor.Cell{Text: label, Choice: &monitor.Choice{Action: chooseAction, ID: id, Value: chosen, Options: opts, Hint: hint}}
}
