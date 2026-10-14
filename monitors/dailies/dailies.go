// Package dailies shows which of today's daily puzzle games one player
// hasn't played yet, from the dailies tracker (a separate app).
package dailies

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("dailies", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// URL is the tracker's address. Requests from the same machine that
	// don't come through its proxy need no login, e.g.
	// "http://127.0.0.1:6040".
	URL string `json:"url"`
	// Player is whose games to show, as the tracker names them.
	Player string `json:"player"`
	// Timezone is the tracker's day, which starts at midnight here. It
	// defaults to Europe/London.
	Timezone string `json:"timezone"`
}

// Monitor is the dailies monitor.
type Monitor struct {
	cfg Config
	loc *time.Location
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.URL == "" || c.Player == "" {
		return nil, fmt.Errorf("dailies needs a url and a player")
	}
	c.URL = strings.TrimRight(c.URL, "/")
	if c.Timezone == "" {
		c.Timezone = "Europe/London"
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, err
	}
	return &Monitor{cfg: c, loc: loc}, nil
}

// game is one daily game, in the order they're usually played.
type game struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type config struct {
	Games []game `json:"games"`
	Today string `json:"today"`
}

type day struct {
	Results []struct {
		Game   string `json:"game"`
		Player string `json:"player"`
	} `json:"results"`
	// Ends are players who've ended the day early, with what's played.
	Ends []struct {
		Player string `json:"player"`
	} `json:"ends"`
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	var c config
	if err := m.get(ctx, env.HTTP, "/api/config", &c); err != nil {
		return monitor.Result{}, err
	}
	var d day
	if err := m.get(ctx, env.HTTP, "/api/day?date="+c.Today, &d); err != nil {
		return monitor.Result{}, err
	}
	played := map[string]bool{}
	for _, r := range d.Results {
		if r.Player == m.cfg.Player {
			played[r.Game] = true
		}
	}
	ended := false
	for _, e := range d.Ends {
		ended = ended || e.Player == m.cfg.Player
	}
	var left []game
	if !ended {
		for _, g := range c.Games {
			if !played[g.ID] {
				left = append(left, g)
			}
		}
	}
	return monitor.Result{
		Blocks:  blocks(left, ended),
		Summary: summary(left, len(c.Games), ended),
		Next:    m.untilTomorrow(env.Now()),
	}, nil
}

// untilTomorrow is how long until just after the tracker's next day
// starts, so the new day's games show straight away.
func (m *Monitor) untilTomorrow(now time.Time) time.Duration {
	t := now.In(m.loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, m.loc)
	return midnight.Sub(now) + 30*time.Second
}

func blocks(left []game, ended bool) []monitor.Block {
	switch {
	case ended:
		return []monitor.Block{{Type: monitor.BlockText, Text: "You ended today early."}}
	case len(left) == 0:
		return []monitor.Block{{Type: monitor.BlockText, Text: "All played for today."}}
	}
	b := monitor.Block{Type: monitor.BlockList, Title: "Still to play", Items: []monitor.Item{}}
	for _, g := range left {
		b.Items = append(b.Items, monitor.Item{Title: g.Name, URL: g.URL})
	}
	return []monitor.Block{b}
}

func summary(left []game, total int, ended bool) []monitor.Stat {
	switch {
	case ended:
		return []monitor.Stat{{Label: "Dailies", Value: "Ended early", Tone: monitor.ToneMuted}}
	case len(left) == 0:
		return []monitor.Stat{{Label: "Dailies", Value: "All played", Tone: monitor.ToneGood}}
	}
	return []monitor.Stat{{Label: "Dailies left", Value: fmt.Sprintf("%d of %d", len(left), total), Detail: "Next: " + left[0].Name}}
}

func (m *Monitor) get(ctx context.Context, client *http.Client, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.URL+path, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dailies %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
