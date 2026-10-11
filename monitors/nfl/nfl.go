// Package nfl shows the week's NFL games from ESPN's scoreboard and
// notifies kick-offs, scores and final results for followed teams.
package nfl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("nfl", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const defaultScoreboardURL = "https://site.api.espn.com/apis/site/v2/sports/football/nfl/scoreboard"

// Config is the monitor's config.
type Config struct {
	// Teams are the abbreviations of followed teams, e.g. ["CHI", "GB"].
	Teams []string `json:"teams"`
	// Scores notifies every score in followed teams' games, not just
	// kick-off and the result. It defaults to true.
	Scores *bool `json:"scores"`
	// AllFinals notifies the result of every game, followed or not.
	AllFinals bool `json:"allFinals"`
	// Timezone is the zone kick-off times are shown in. It defaults to the
	// server's.
	Timezone string `json:"timezone"`
	// LiveInterval is how often to poll while a game is on. It defaults to
	// one minute.
	LiveInterval monitor.Duration `json:"liveInterval"`
	// ScoreboardURL replaces ESPN's endpoint, for tests.
	ScoreboardURL string `json:"scoreboardURL"`
}

// Monitor is the NFL monitor.
type Monitor struct {
	cfg    Config
	teams  map[string]bool
	scores bool
	zone   *time.Location
	live   time.Duration
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	m := &Monitor{cfg: c, teams: map[string]bool{}, scores: true, zone: time.Local, live: time.Minute}
	for _, t := range c.Teams {
		m.teams[strings.ToUpper(t)] = true
	}
	if c.Scores != nil {
		m.scores = *c.Scores
	}
	if c.Timezone != "" {
		z, err := time.LoadLocation(c.Timezone)
		if err != nil {
			return nil, err
		}
		m.zone = z
	}
	if c.LiveInterval > 0 {
		m.live = time.Duration(c.LiveInterval)
	}
	if m.cfg.ScoreboardURL == "" {
		m.cfg.ScoreboardURL = defaultScoreboardURL
	}
	return m, nil
}

type scoreboard struct {
	Week struct {
		Number int `json:"number"`
	} `json:"week"`
	Events []struct {
		ID           string    `json:"id"`
		Date         espnTime  `json:"date"`
		Competitions []contest `json:"competitions"`
	} `json:"events"`
}

type contest struct {
	Competitors []struct {
		HomeAway string `json:"homeAway"`
		Score    string `json:"score"`
		Team     struct {
			Abbreviation     string `json:"abbreviation"`
			ShortDisplayName string `json:"shortDisplayName"`
		} `json:"team"`
	} `json:"competitors"`
	Status struct {
		Type struct {
			State       string `json:"state"` // pre, in or post
			ShortDetail string `json:"shortDetail"`
		} `json:"type"`
	} `json:"status"`
	Situation *struct {
		DownDistanceText string `json:"downDistanceText"`
	} `json:"situation"`
	Broadcasts []struct {
		Names []string `json:"names"`
	} `json:"broadcasts"`
}

// espnTime parses ESPN's dates, which leave out seconds: "2026-10-11T17:00Z".
type espnTime struct{ time.Time }

func (t *espnTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.Parse("2006-01-02T15:04Z07:00", s)
	if err != nil {
		v, err = time.Parse(time.RFC3339, s)
	}
	t.Time = v
	return err
}

// game is one game, flattened.
type game struct {
	id                   string
	kickoff              time.Time
	state, detail        string
	away, home           string // abbreviations
	awayName, homeName   string
	awayScore, homeScore string
	broadcast            string
}

func (g game) score() string {
	return fmt.Sprintf("%s %s – %s %s", g.away, g.awayScore, g.homeScore, g.home)
}

func (m *Monitor) fetch(ctx context.Context, client *http.Client) (int, []game, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.ScoreboardURL, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("scoreboard returned %s", resp.Status)
	}
	var sb scoreboard
	if err := json.NewDecoder(resp.Body).Decode(&sb); err != nil {
		return 0, nil, fmt.Errorf("decoding scoreboard: %w", err)
	}
	var games []game
	for _, e := range sb.Events {
		if len(e.Competitions) == 0 {
			continue
		}
		c := e.Competitions[0]
		g := game{id: e.ID, kickoff: e.Date.Time, state: c.Status.Type.State, detail: c.Status.Type.ShortDetail}
		for _, t := range c.Competitors {
			if t.HomeAway == "home" {
				g.home, g.homeName, g.homeScore = t.Team.Abbreviation, t.Team.ShortDisplayName, t.Score
			} else {
				g.away, g.awayName, g.awayScore = t.Team.Abbreviation, t.Team.ShortDisplayName, t.Score
			}
		}
		if c.Situation != nil && c.Situation.DownDistanceText != "" && g.state == "in" {
			g.detail += " · " + c.Situation.DownDistanceText
		}
		for _, b := range c.Broadcasts {
			if len(b.Names) > 0 {
				g.broadcast = b.Names[0]
				break
			}
		}
		games = append(games, g)
	}
	return sb.Week.Number, games, nil
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	week, games, err := m.fetch(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}
	var events []monitor.Event
	for _, g := range games {
		events = append(events, m.gameEvents(g)...)
	}
	summary, style := m.today(games, env.Now())
	if summary == nil {
		summary, style = m.summary(week, games), monitor.SummaryTiles
	}
	return monitor.Result{
		Blocks:       []monitor.Block{m.table(week, games)},
		Summary:      summary,
		SummaryStyle: style,
		Events:       events,
		Next:         m.next(games, env.Now()),
	}, nil
}

func (m *Monitor) followed(g game) bool {
	return m.teams[g.away] || m.teams[g.home]
}

// gameEvents describes the state of a game as events. Keys are stable for
// each state and score, so the engine only passes on what's changed.
func (m *Monitor) gameEvents(g game) []monitor.Event {
	url := "https://www.espn.com/nfl/game/_/gameId/" + g.id
	followed := m.followed(g)
	switch g.state {
	case "in":
		if !followed {
			return nil
		}
		events := []monitor.Event{{
			Key:   "kickoff:" + g.id,
			Title: fmt.Sprintf("%s @ %s has kicked off", g.away, g.home),
			Body:  g.broadcast,
			URL:   url,
		}}
		if m.scores && (g.awayScore != "0" || g.homeScore != "0") {
			events = append(events, monitor.Event{
				Key:   fmt.Sprintf("score:%s:%s-%s", g.id, g.awayScore, g.homeScore),
				Title: g.score(),
				Body:  g.detail,
				URL:   url,
			})
		}
		return events
	case "post":
		if !followed && !m.cfg.AllFinals {
			return nil
		}
		prio := monitor.PriorityLow
		if followed {
			prio = monitor.PriorityHigh
		}
		return []monitor.Event{{
			Key:      "final:" + g.id,
			Title:    "Final: " + g.score(),
			Body:     g.detail,
			URL:      url,
			Priority: prio,
		}}
	}
	return nil
}

// next polls every LiveInterval while a game is on or due to start, and
// otherwise wakes in time for the next kick-off.
func (m *Monitor) next(games []game, now time.Time) time.Duration {
	var soonest time.Duration
	for _, g := range games {
		switch {
		case g.state == "in", g.state == "pre" && !g.kickoff.After(now):
			return m.live
		case g.state == "pre":
			if d := g.kickoff.Sub(now); soonest == 0 || d < soonest {
				soonest = d
			}
		}
	}
	// The engine only uses this if it's sooner than the interval.
	return soonest
}

func (m *Monitor) table(week int, games []game) monitor.Block {
	b := monitor.Block{
		Type:  monitor.BlockTable,
		Title: fmt.Sprintf("Week %d", week),
		Columns: []monitor.Column{
			{Title: "Away"},
			{Title: "", Align: monitor.AlignRight},
			{Title: "Home"},
			{Title: "", Align: monitor.AlignRight},
			{Title: "Status"},
		},
		Rows: [][]monitor.Cell{},
	}
	for _, g := range games {
		team := func(abbr, name string) monitor.Cell {
			c := monitor.Cell{Text: name}
			if m.teams[abbr] {
				c.Tone = monitor.ToneAccent
			}
			return c
		}
		awayScore, homeScore := g.awayScore, g.homeScore
		status := monitor.Cell{Text: g.detail, URL: "https://www.espn.com/nfl/game/_/gameId/" + g.id}
		switch g.state {
		case "pre":
			awayScore, homeScore = "", ""
			status.Text = g.kickoff.In(m.zone).Format("Mon 15:04")
			if g.broadcast != "" {
				status.Text += " " + g.broadcast
			}
			status.Tone = monitor.ToneMuted
		case "in":
			status.Tone = monitor.ToneWarn
		}
		awayCell, homeCell := monitor.Cell{Text: awayScore}, monitor.Cell{Text: homeScore}
		if g.state == "post" {
			if n(awayScore) > n(homeScore) {
				awayCell.Tone = monitor.ToneGood
			} else if n(homeScore) > n(awayScore) {
				homeCell.Tone = monitor.ToneGood
			}
		}
		b.Rows = append(b.Rows, []monitor.Cell{
			team(g.away, g.awayName), awayCell, team(g.home, g.homeName), homeCell, status,
		})
	}
	return b
}

// newYork is where the NFL's day is reckoned: a Sunday night game that
// kicks off after midnight in the UK is still Sunday's.
var newYork = func() *time.Location {
	l, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return l
}()

// today is a chip for each of today's games, in kick-off order: the teams
// stacked, away above home, to keep chips narrow, then the kick-off time
// and channel until it starts, the score and clock while it's on, and the
// final score. Followed teams' games are highlighted, and live ones
// marked. It's nil on a day without games.
func (m *Monitor) today(games []game, now time.Time) ([]monitor.Stat, monitor.SummaryStyle) {
	day := now.In(newYork).Format("2006-01-02")
	var todays []game
	for _, g := range games {
		if g.kickoff.In(newYork).Format("2006-01-02") == day || g.state == "in" {
			todays = append(todays, g)
		}
	}
	if len(todays) == 0 {
		return nil, ""
	}
	sort.SliceStable(todays, func(i, j int) bool { return todays[i].kickoff.Before(todays[j].kickoff) })
	var out []monitor.Stat
	for _, g := range todays {
		s := monitor.Stat{Label: g.away + "\n" + g.home, Value: g.awayScore + "–" + g.homeScore}
		switch g.state {
		case "pre":
			s.Value = g.kickoff.In(m.zone).Format("15:04")
			s.Detail = g.broadcast
		case "in":
			s.Detail = clock(g.detail)
			s.Tone = monitor.ToneWarn
		case "post":
			s.Detail = "Final"
			s.Tone = monitor.ToneMuted
		}
		if m.followed(g) {
			s.Tone = monitor.ToneAccent
		}
		out = append(out, s)
	}
	return out, monitor.SummaryChips
}

// clock shortens a live game's status for a chip: "7:42 - 2nd · 2nd & 6
// at GB 41" becomes "2nd 7:42"; others, such as "Halftime", stay.
func clock(detail string) string {
	status, _, _ := strings.Cut(detail, " · ")
	if t, quarter, ok := strings.Cut(status, " - "); ok {
		return quarter + " " + t
	}
	return status
}

// summary is the followed teams' games this week: the score once they've
// kicked off, otherwise when they do. Without any, it counts live games.
func (m *Monitor) summary(week int, games []game) []monitor.Stat {
	var out []monitor.Stat
	live := 0
	for _, g := range games {
		if g.state == "in" {
			live++
		}
		if !m.followed(g) {
			continue
		}
		s := monitor.Stat{Label: g.away + " @ " + g.home, Value: g.awayScore + "–" + g.homeScore, Detail: g.detail}
		switch g.state {
		case "pre":
			s.Value = g.kickoff.In(m.zone).Format("Mon 15:04")
			s.Detail = g.broadcast
		case "in":
			s.Tone = monitor.ToneWarn
		case "post":
			// Green for a followed team's win, red for a loss.
			ours, theirs := n(g.homeScore), n(g.awayScore)
			if m.teams[g.away] {
				ours, theirs = theirs, ours
			}
			switch {
			case ours > theirs:
				s.Tone = monitor.ToneGood
			case ours < theirs:
				s.Tone = monitor.ToneBad
			}
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		s := monitor.Stat{Label: fmt.Sprintf("Week %d", week), Value: "No games on", Tone: monitor.ToneMuted}
		if live > 0 {
			s.Value, s.Tone = fmt.Sprintf("%d live", live), monitor.ToneWarn
		}
		out = append(out, s)
	}
	return out
}

func n(s string) int {
	var v int
	fmt.Sscan(s, &v)
	return v
}
