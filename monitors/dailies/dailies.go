// Package dailies shows today's daily puzzle games and which of them one
// player has played, from the dailies tracker (a separate app), and logs
// results pasted into the dashboard.
package dailies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	// Versus, if set, is who Player is up against: played games are then
	// green when won, red when lost and blue when drawn or waiting on them.
	Versus string `json:"versus"`
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
	// Winners is who won each game both have played, "" for a tie.
	Winners map[string]string `json:"winners"`
	// Coop is set on a co-op day, played as a team to targets: for each
	// game played, whether each player hit its target.
	Coop *struct {
		Targets map[string]map[string]bool `json:"targets"`
	} `json:"coop"`
}

// outcome is where a game stands for the player today.
type outcome int

const (
	unplayed outcome = iota
	skipped          // the player ended the day without it
	done             // played, with no one to compare against
	waiting          // played; the opponent hasn't yet
	won
	lost
	drew
	hit    // a co-op day's target reached
	missed // a co-op day's target missed
)

// status is the panel's word for an outcome, and its tone.
func (m *Monitor) status(o outcome) monitor.Cell {
	switch o {
	case skipped:
		return monitor.Cell{Text: "Skipped", Tone: monitor.ToneMuted}
	case done:
		return monitor.Cell{Text: "Done", Tone: monitor.ToneGood}
	case waiting:
		return monitor.Cell{Text: "Waiting for " + m.cfg.Versus, Tone: monitor.ToneAccent}
	case won:
		return monitor.Cell{Text: "Won", Tone: monitor.ToneGood}
	case lost:
		return monitor.Cell{Text: "Lost", Tone: monitor.ToneBad}
	case hit:
		return monitor.Cell{Text: "Hit target", Tone: monitor.ToneGood}
	case missed:
		return monitor.Cell{Text: "Missed target", Tone: monitor.ToneBad}
	case drew:
		return monitor.Cell{Text: "Drew", Tone: monitor.ToneAccent}
	}
	return monitor.Cell{} // blank until played, so the done ones stand out
}

// outcomes works out where each game stands for the player; games not in
// it are unplayed, or skipped if they've ended the day.
func (m *Monitor) outcomes(d day) (got map[string]outcome, ended bool) {
	played := map[string]map[string]bool{}
	for _, r := range d.Results {
		if played[r.Player] == nil {
			played[r.Player] = map[string]bool{}
		}
		played[r.Player][r.Game] = true
	}
	for _, e := range d.Ends {
		ended = ended || e.Player == m.cfg.Player
	}
	got = map[string]outcome{}
	for g := range played[m.cfg.Player] {
		winner, both := d.Winners[g]
		switch {
		case d.Coop != nil && d.Coop.Targets[g][m.cfg.Player]:
			got[g] = hit
		case d.Coop != nil:
			got[g] = missed
		case m.cfg.Versus == "":
			got[g] = done
		case !played[m.cfg.Versus][g] || !both:
			got[g] = waiting
		case winner == m.cfg.Player:
			got[g] = won
		case winner == "":
			got[g] = drew
		default:
			got[g] = lost
		}
	}
	return got, ended
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
	got, ended := m.outcomes(d)
	// What's been pasted since the last poll; the engine polls straight
	// after an action.
	var outcomes []monitor.Event
	if _, err := env.State.Get(outcomesKey, &outcomes); err != nil {
		return monitor.Result{}, err
	}
	if len(outcomes) > 0 {
		if err := env.State.Set(outcomesKey, []monitor.Event{}); err != nil {
			return monitor.Result{}, err
		}
	}
	return monitor.Result{
		Paste:        "Log a result from the clipboard",
		Events:       outcomes,
		Blocks:       []monitor.Block{m.list(c.Games, got, ended)},
		Summary:      m.chips(c.Games, got, ended),
		SummaryStyle: monitor.SummaryChips,
		Next:         m.untilTomorrow(env.Now()),
	}, nil
}

const outcomesKey = "outcomes"

// outcomeFor is how long a paste's outcome is shown.
const outcomeFor = 2 * time.Minute

// Act takes a "paste" action: share text from the clipboard, logged as
// today's result for the player. The tracker works out the game and
// score. Clients don't show an action's reply, so what happened, logged
// or not, is a temporary event at the next poll.
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != "paste" {
		return fmt.Errorf("%w: dailies only takes pasted share text", monitor.ErrBadAction)
	}
	// Only worth seeing now: the user's just pressed the button.
	ev := monitor.Event{Key: fmt.Sprintf("paste:%d", env.Now().UnixNano()), Quiet: true, Expires: env.Now().Add(outcomeFor)}
	msg, err := m.share(ctx, env.HTTP, a.Text)
	var refused refusal
	switch {
	case errors.As(err, &refused):
		ev.Title, ev.Body = "Not logged", string(refused)
	case err != nil:
		return err
	default:
		ev.Title = msg
	}
	var outcomes []monitor.Event
	if _, err := env.State.Get(outcomesKey, &outcomes); err != nil {
		return err
	}
	return env.State.Set(outcomesKey, append(outcomes, ev))
}

// refusal is the tracker turning down share text, with its reason.
type refusal string

func (r refusal) Error() string { return string(r) }

// share sends share text to the tracker, returning its message.
func (m *Monitor) share(ctx context.Context, client *http.Client, text string) (string, error) {
	body, _ := json.Marshal(map[string]string{"player": m.cfg.Player, "text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.URL+"/api/share", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("dailies /api/share: %s", resp.Status)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return out.Message, nil
	case resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity:
		return "", refusal(out.Error)
	}
	return "", fmt.Errorf("dailies /api/share: %s: %s", resp.Status, out.Error)
}

// untilTomorrow is how long until just after the tracker's next day
// starts, so the new day's games show straight away.
func (m *Monitor) untilTomorrow(now time.Time) time.Duration {
	t := now.In(m.loc)
	midnight := time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, m.loc)
	return midnight.Sub(now) + 30*time.Second
}

// outcomeOf is where game id stands, from what outcomes worked out.
func outcomeOf(got map[string]outcome, ended bool, id string) outcome {
	if o, ok := got[id]; ok {
		return o
	}
	if ended {
		return skipped
	}
	return unplayed
}

// list is every game, each linking to it and saying where it stands, one
// line each.
func (m *Monitor) list(games []game, got map[string]outcome, ended bool) monitor.Block {
	b := monitor.Block{Type: monitor.BlockTable, Columns: []monitor.Column{{Title: "Game", Fill: true}, {Title: "Today"}}, Rows: [][]monitor.Cell{}}
	for _, g := range games {
		b.Rows = append(b.Rows, []monitor.Cell{{Text: g.Name, URL: g.URL}, m.status(outcomeOf(got, ended, g.ID))})
	}
	return b
}

// chips is a tile for each game, by its short name if it has one, in the
// colour of where it stands.
func (m *Monitor) chips(games []game, got map[string]outcome, ended bool) []monitor.Stat {
	out := make([]monitor.Stat, len(games))
	for i, g := range games {
		st := monitor.Stat{Value: g.Name, URL: g.URL, Tone: m.status(outcomeOf(got, ended, g.ID)).Tone}
		if short := m.cfg.Names[g.ID]; short != "" {
			st.Value = short
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
