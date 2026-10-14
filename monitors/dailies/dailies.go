// Package dailies shows today's daily puzzle games and which of them one
// player has played, from the dailies tracker (a separate app).
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
	// Names gives games shorter names for the summary's tiles, by the tracker's
	// game ID, e.g. {"wordle": "W"}.
	Names map[string]string `json:"names"`
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
	return monitor.Result{
		Blocks:       []monitor.Block{list(c.Games, played, ended)},
		Summary:      m.chips(c.Games, played, ended),
		SummaryStyle: monitor.SummaryChips,
		Next:         m.untilTomorrow(env.Now()),
	}, nil
}

// untilTomorrow is how long until just after the tracker's next day
// starts, so the new day's games show straight away.
func (m *Monitor) untilTomorrow(now time.Time) time.Duration {
	t := now.In(m.loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, m.loc)
	return midnight.Sub(now) + 30*time.Second
}

// list is every game, each linking to it and saying whether it's been
// played, one line each.
func list(games []game, played map[string]bool, ended bool) monitor.Block {
	b := monitor.Block{Type: monitor.BlockTable, Columns: []monitor.Column{{Title: "Game", Fill: true}, {Title: "Today"}}, Rows: [][]monitor.Cell{}}
	for _, g := range games {
		var status monitor.Cell // blank until played, so the done ones stand out
		switch {
		case played[g.ID]:
			status = monitor.Cell{Text: "Done", Tone: monitor.ToneGood}
		case ended:
			status = monitor.Cell{Text: "Skipped", Tone: monitor.ToneMuted}
		}
		b.Rows = append(b.Rows, []monitor.Cell{{Text: g.Name, URL: g.URL}, status})
	}
	return b
}

// chips is a tile for each game, by its short name if it has one, green
// once it's been played.
func (m *Monitor) chips(games []game, played map[string]bool, ended bool) []monitor.Stat {
	out := make([]monitor.Stat, len(games))
	for i, g := range games {
		st := monitor.Stat{Value: g.Name, URL: g.URL}
		if short := m.cfg.Names[g.ID]; short != "" {
			st.Value = short
		}
		switch {
		case played[g.ID]:
			st.Tone = monitor.ToneGood
		case ended:
			st.Tone = monitor.ToneMuted
		}
		out[i] = st
	}
	return out
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
