package ai

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// DefaultClaudeModel is the Claude model a Use gets until one is chosen:
// the cheapest current one, as the monitors' jobs are small.
const DefaultClaudeModel = "claude-haiku-5-5"

// Use is one job a monitor gives a model, e.g. reading mail.
type Use struct {
	// ID names the Use in Settings; the monitor's kind, e.g. "mail".
	ID string
	// Label is what clients call it, e.g. "Mail".
	Label string
	// Gemini is the Gemini model to use until one is chosen.
	Gemini string
}

// Choice is who a Use goes to, and which model at each provider.
type Choice struct {
	Provider Provider `json:"provider,omitempty"`
	Gemini   string   `json:"gemini,omitempty"`
	Claude   string   `json:"claude,omitempty"`
}

// State is where Settings keeps choices; a monitor.State fits.
type State interface {
	Get(key string, v any) (bool, error)
	Set(key string, v any) error
}

// choicesKey is the state key holding the choices, by Use ID.
const choicesKey = "choices"

// Settings keeps which provider and models each Use has. It's safe for
// concurrent use.
type Settings struct {
	mu      sync.Mutex
	uses    []Use
	choices map[string]Choice
	state   State
}

// Shared is the process's Settings, which monitors' Clients use.
var Shared = &Settings{}

// Register adds u, or updates it if its ID is known.
func (s *Settings) Register(u Use) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i := slices.IndexFunc(s.uses, func(x Use) bool { return x.ID == u.ID }); i >= 0 {
		s.uses[i] = u
		return
	}
	s.uses = append(s.uses, u)
}

// Attach loads saved choices from st and saves later ones there.
func (s *Settings) Attach(st State) error {
	saved := map[string]Choice{}
	if _, err := st.Get(choicesKey, &saved); err != nil {
		return fmt.Errorf("loading AI choices: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state, s.choices = st, saved
	return nil
}

// Uses lists the registered Uses, in the order registered.
func (s *Settings) Uses() []Use {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.uses)
}

// Choice is what id goes to, with defaults filled in.
func (s *Settings) Choice(id string) Choice {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.choice(id)
}

func (s *Settings) choice(id string) Choice {
	c := s.choices[id]
	if c.Provider != Claude {
		c.Provider = Gemini
	}
	if c.Gemini == "" {
		if i := slices.IndexFunc(s.uses, func(u Use) bool { return u.ID == id }); i >= 0 {
			c.Gemini = s.uses[i].Gemini
		}
	}
	if c.Claude == "" {
		c.Claude = DefaultClaudeModel
	}
	return c
}

// Set changes one part of id's choice: what is "provider", "gemini" or
// "claude", and value the provider or model.
func (s *Settings) Set(id, what, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !slices.ContainsFunc(s.uses, func(u Use) bool { return u.ID == id }) {
		return fmt.Errorf("no AI use %q", id)
	}
	if value == "" {
		return fmt.Errorf("no %s given", what)
	}
	c := s.choice(id)
	switch what {
	case "provider":
		p := Provider(value)
		if p != Gemini && p != Claude {
			return fmt.Errorf("unknown provider %q", value)
		}
		c.Provider = p
	case "gemini":
		c.Gemini = value
	case "claude":
		c.Claude = value
	default:
		return fmt.Errorf("can't set %q", what)
	}
	next := make(map[string]Choice, len(s.choices)+1)
	maps.Copy(next, s.choices)
	next[id] = c
	if s.state != nil {
		if err := s.state.Set(choicesKey, next); err != nil {
			return err
		}
	}
	s.choices = next
	return nil
}
