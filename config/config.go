// Package config loads kitchensink's JSON config file.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"

	"github.com/seventy-two/kitchensink/monitor"
)

// Config is the whole config file.
type Config struct {
	// Listen is the address to serve the API on, e.g. "127.0.0.1:6030".
	Listen string `json:"listen"`
	// Database is the SQLite file path.
	Database string `json:"database"`
	// Token is the bearer token clients must send. Use "${ENV_VAR}" to keep
	// it out of the file.
	Token     string     `json:"token"`
	Notifiers []Notifier `json:"notifiers"`
	Monitors  []Monitor  `json:"monitors"`
}

// Monitor configures one monitor.
type Monitor struct {
	// ID names this monitor in the API and event log. It defaults to Type,
	// so it only needs setting when one type is used twice.
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Title    string           `json:"title"`
	Interval monitor.Duration `json:"interval"`
	// Disabled monitors are skipped without removing their config.
	Disabled bool            `json:"disabled"`
	Config   json.RawMessage `json:"config"`
}

// Notifier configures an outside notification channel.
type Notifier struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// Load reads the config file at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, os.LookupEnv)
}

var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Parse parses a config file. Each "${NAME}" is replaced with that
// environment variable, JSON-escaped, so secrets and personal details can
// live in the environment instead of the file. Unset variables are an
// error.
func Parse(b []byte, lookup func(string) (string, bool)) (*Config, error) {
	var missing []string
	b = envRef.ReplaceAllFunc(b, func(m []byte) []byte {
		name := string(envRef.FindSubmatch(m)[1])
		v, ok := lookup(name)
		if !ok {
			missing = append(missing, name)
			return nil
		}
		// Escape as the inside of a JSON string.
		q, _ := json.Marshal(v)
		return q[1 : len(q)-1]
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("config uses unset environment variables: %v", missing)
	}

	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	c := &Config{}
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:6030"
	}
	if c.Database == "" {
		c.Database = "kitchensink.db"
	}
	if c.Token == "" {
		return nil, fmt.Errorf("config needs a token")
	}
	for i := range c.Monitors {
		m := &c.Monitors[i]
		if m.Type == "" {
			return nil, fmt.Errorf("monitor %d has no type", i)
		}
		if m.ID == "" {
			m.ID = m.Type
		}
		if m.Title == "" {
			m.Title = m.ID
		}
	}
	return c, nil
}
