// Package fantasy follows the user's fantasy football teams on Sleeper and
// ESPN: their matchup scores, and notable plays (touchdowns, big gains,
// turnovers, long or missed kicks, defensive scores) by players on any of
// their teams, from ESPN's live play-by-play. Plays are only listed in the
// panel; the one alert is a player of the user's reaching BigDay points
// in the week (in any league).
package fantasy

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("fantasy", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// ESPNLeague is an ESPN fantasy league.
type ESPNLeague struct {
	LeagueID string `json:"leagueId"`
	// S2 and SWID are the espn_s2 and SWID cookies from a browser signed
	// in to ESPN, needed for private leagues; keep them in the environment.
	// SWID also says which team is the user's.
	S2   string `json:"s2"`
	SWID string `json:"swid"`
}

func (l ESPNLeague) cookie() string { return "espn_s2=" + l.S2 + "; SWID=" + l.SWID }

// Config is the monitor's config.
type Config struct {
	// SleeperUser is a Sleeper username; all of its leagues this season
	// are followed.
	SleeperUser string       `json:"sleeperUser"`
	ESPN        []ESPNLeague `json:"espn"`
	// Names gives leagues shorter names, by league ID.
	Names map[string]string `json:"names"`
	// BigPlayYards is the gain that counts as a big play. It defaults to
	// 20.
	BigPlayYards int `json:"bigPlayYards"`
	// BigDay is the fantasy points in a week at which one of the user's
	// players is notified, once. It defaults to 30.
	BigDay float64 `json:"bigDay"`
	// LiveInterval is how often to check while the user's players are
	// playing. It defaults to 30 seconds.
	LiveInterval monitor.Duration `json:"liveInterval"`
	// URLs replace the APIs' addresses, for tests.
	SleeperURL string `json:"sleeperURL"`
	ESPNURL    string `json:"espnURL"`
	SiteURL    string `json:"siteURL"`
	CoreURL    string `json:"coreURL"`
}

// Monitor is the fantasy monitor.
type Monitor struct {
	cfg  Config
	live time.Duration
	// teams are the NFL teams the user has players or a defence on, as of
	// the last poll.
	teams []string
}

// Teams are the NFL teams the user has players or a defence on, as of the
// last poll, by abbreviation.
func (m *Monitor) Teams() []string { return m.teams }

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{
		BigPlayYards: 20,
		BigDay:       30,
		LiveInterval: monitor.Duration(30 * time.Second),
		SleeperURL:   "https://api.sleeper.app/v1",
		ESPNURL:      "https://lm-api-reads.fantasy.espn.com/apis/v3/games/ffl",
		SiteURL:      "https://site.api.espn.com/apis/site/v2/sports/football/nfl",
		CoreURL:      "https://sports.core.api.espn.com/v2/sports/football/leagues/nfl",
	}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.SleeperUser == "" && len(c.ESPN) == 0 {
		return nil, fmt.Errorf("fantasy needs a sleeperUser or an espn league")
	}
	for _, l := range c.ESPN {
		if l.LeagueID == "" || l.SWID == "" {
			return nil, fmt.Errorf("each espn league needs a leagueId and swid")
		}
	}
	return &Monitor{cfg: c, live: time.Duration(c.LiveInterval)}, nil
}

// State keys.
const (
	rosterKey = "roster"
	gamesKey  = "games"
	playsKey  = "plays"
)

// rosterEvery is how often rosters are read again, to pick up trades and
// waiver moves.
const rosterEvery = 6 * time.Hour

// weekEvery is how often, while the week just played is still shown,
// Sleeper is asked whether its app has moved on to the new week.
const weekEvery = 15 * time.Minute

type savedRoster struct {
	Read   time.Time `json:"read"`
	Roster *roster   `json:"roster"`
	// Checked is when Sleeper's shown week was last asked for.
	Checked time.Time `json:"checked,omitempty"`
}

// gameState is what's been seen of one game.
type gameState struct {
	Seen []string `json:"seen"` // play IDs
	Done bool     `json:"done"` // over, with its plays read
}

// noted is a notable play, kept for the panel until the week ends.
type noted struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Good    bool      `json:"good"`
	Text    string    `json:"text"`
	Game    string    `json:"game"`
	When    string    `json:"when"`
	Leagues []string  `json:"leagues"`
	Week    int       `json:"week"`
	At      time.Time `json:"at"`
}

// maxNoted bounds the plays kept for the panel.
const maxNoted = 60

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	r, err := m.roster(ctx, env, now)
	if err != nil {
		return monitor.Result{}, err
	}

	var games map[string]gameState
	var notes []noted
	if _, err := env.State.Get(gamesKey, &games); err != nil {
		return monitor.Result{}, err
	}
	if _, err := env.State.Get(playsKey, &notes); err != nil {
		return monitor.Result{}, err
	}
	if games == nil {
		games = map[string]gameState{}
	}

	board, err := m.scoreboard(ctx, env)
	if err != nil {
		return monitor.Result{}, err
	}
	watched := map[string]bool{}
	for _, p := range r.Players {
		watched[p.Team] = true
	}
	for t := range r.Defences {
		watched[t] = true
	}
	m.teams = m.teams[:0]
	for t := range watched {
		m.teams = append(m.teams, t)
	}
	sort.Strings(m.teams)

	var next time.Duration
	live := false // whether any of the user's players' games are on
	for _, g := range board {
		if !watched[g.Home] && !watched[g.Away] {
			continue
		}
		gs := games[g.ID]
		switch {
		case g.State == "pre":
			if d := g.Kickoff.Sub(now); d > 0 && (next == 0 || d < next) {
				next = d
			}
			continue
		case g.State == "in":
			next, live = m.live, true
		case gs.Done:
			continue
		}
		plays, err := m.newPlays(ctx, env, g.ID, gs.Seen)
		if err != nil {
			env.Log.Warn("reading plays", "game", g.Name, "err", err)
			continue
		}
		for _, p := range plays {
			gs.Seen = append(gs.Seen, p.ID)
			defence := g.Home
			if p.Start.Team.id() == g.HomeID {
				defence = g.Away
			}
			for _, sig := range classify(p, defence, r.Players, r.Defences, m.cfg.BigPlayYards) {
				n := noted{ID: p.ID, Kind: sig.Kind, Good: sig.Good, Text: describe(p), Game: g.Name, When: when(p),
					Leagues: sig.Leagues, Week: r.Week, At: now}
				notes = append(notes, n)
			}
		}
		gs.Done = g.State == "post"
		games[g.ID] = gs
	}

	// Keep only this week's games, and the plays since the shown week:
	// last night's stay while its results are shown.
	weekGames := map[string]gameState{}
	for _, g := range board {
		if gs, ok := games[g.ID]; ok {
			weekGames[g.ID] = gs
		}
	}
	notes = slices.DeleteFunc(notes, func(n noted) bool { return n.Week < r.shownWeek() || n.Week > r.Week })
	if len(notes) > maxNoted {
		notes = notes[len(notes)-maxNoted:]
	}
	if err := env.State.Set(gamesKey, weekGames); err != nil {
		return monitor.Result{}, err
	}
	if err := env.State.Set(playsKey, notes); err != nil {
		return monitor.Result{}, err
	}

	scores, players := m.matchups(ctx, env.HTTP, r, env.Log.Warn)
	return monitor.Result{
		Blocks:       blocks(scores, notes),
		Summary:      summary(scores),
		SummaryStyle: monitor.SummaryChips,
		Events:       bigDays(players, r.Season, r.Week, m.cfg.BigDay),
		Next:         next,
		Timely:       live,
	}, nil
}

// roster returns the user's rosters, reading them again every few hours.
// If that fails, the last ones read do.
func (m *Monitor) roster(ctx context.Context, env monitor.Env, now time.Time) (*roster, error) {
	var saved savedRoster
	if _, err := env.State.Get(rosterKey, &saved); err != nil {
		return nil, err
	}
	// One saved before the shown week was kept is read again.
	if saved.Roster != nil && saved.Roster.Shown > 0 && now.Sub(saved.Read) < rosterEvery && saved.Roster.hasNames() {
		r := saved.Roster
		if r.shownWeek() == r.Week || now.Sub(laterOf(saved.Read, saved.Checked)) < weekEvery {
			return r, nil
		}
		// Still showing the week just played: move on when Sleeper does.
		var state sleeperState
		if err := getJSON(ctx, env.HTTP, m.cfg.SleeperURL+"/state/nfl", &state); err != nil {
			env.Log.Warn("reading the shown week", "err", err)
			return r, nil
		}
		if state.Season == r.Season && state.Week == r.Week {
			if state.DisplayWeek > r.Shown && state.DisplayWeek <= r.Week {
				r.Shown = state.DisplayWeek
			}
			saved.Checked = now
			if err := env.State.Set(rosterKey, saved); err != nil {
				return nil, err
			}
			return r, nil
		}
		// The NFL's week has moved on as well: read everything again.
	}
	r, err := m.loadRoster(ctx, env.HTTP)
	if err != nil {
		if saved.Roster != nil {
			env.Log.Warn("reading rosters; using the last ones", "err", err)
			return saved.Roster, nil
		}
		return nil, err
	}
	if err := env.State.Set(rosterKey, savedRoster{Read: now, Roster: r}); err != nil {
		return nil, err
	}
	return r, nil
}

// game is one game on ESPN's scoreboard.
type game struct {
	ID, Name       string
	State          string // pre, in or post
	Kickoff        time.Time
	Home, Away     string // abbreviations
	HomeID, AwayID string
}

func (m *Monitor) scoreboard(ctx context.Context, env monitor.Env) ([]game, error) {
	var data struct {
		Events []struct {
			ID           string `json:"id"`
			ShortName    string `json:"shortName"`
			Date         string `json:"date"`
			Competitions []struct {
				Competitors []struct {
					HomeAway string `json:"homeAway"`
					Team     struct {
						ID   string `json:"id"`
						Abbr string `json:"abbreviation"`
					} `json:"team"`
				} `json:"competitors"`
				Status struct {
					Type struct {
						State string `json:"state"`
					} `json:"type"`
				} `json:"status"`
			} `json:"competitions"`
		} `json:"events"`
	}
	if err := getJSON(ctx, env.HTTP, m.cfg.SiteURL+"/scoreboard", &data); err != nil {
		return nil, err
	}
	var out []game
	for _, e := range data.Events {
		if len(e.Competitions) == 0 {
			continue
		}
		c := e.Competitions[0]
		g := game{ID: e.ID, Name: e.ShortName, State: c.Status.Type.State}
		g.Kickoff, _ = time.Parse("2006-01-02T15:04Z07:00", e.Date)
		for _, t := range c.Competitors {
			if t.HomeAway == "home" {
				g.Home, g.HomeID = t.Team.Abbr, t.Team.ID
			} else {
				g.Away, g.AwayID = t.Team.Abbr, t.Team.ID
			}
		}
		out = append(out, g)
	}
	return out, nil
}

// playsPage is how many plays are asked for at a time.
const playsPage = 50

// newPlays reads a game's plays not yet seen. It reads the last two pages
// only, which covers well over a minute's plays.
func (m *Monitor) newPlays(ctx context.Context, env monitor.Env, id string, seen []string) ([]play, error) {
	base := m.cfg.CoreURL + "/events/" + id + "/competitions/" + id + "/plays"
	var head struct {
		Count int `json:"count"`
	}
	if err := getJSON(ctx, env.HTTP, base+"?limit=1", &head); err != nil {
		return nil, err
	}
	last := (head.Count + playsPage - 1) / playsPage
	var all []play
	for page := max(1, last-1); page <= last; page++ {
		var data struct {
			Items []play `json:"items"`
		}
		if err := getJSON(ctx, env.HTTP, base+"?limit="+strconv.Itoa(playsPage)+"&page="+strconv.Itoa(page), &data); err != nil {
			return nil, err
		}
		all = append(all, data.Items...)
	}
	var out []play
	for _, p := range all {
		if !slices.Contains(seen, p.ID) {
			out = append(out, p)
		}
	}
	return out, nil
}

// bigDays is an event for each of the user's players with at least
// limit points this week in any league, keyed by season, week and player
// so the engine sends each only once. A player in several leagues is one
// event, with each league's points.
func bigDays(players []scored, season string, week int, limit float64) []monitor.Event {
	type day struct {
		name   string
		best   float64
		scores []string
	}
	byKey := map[string]*day{}
	var order []string
	for _, p := range players {
		k := nameKey(p.Name, "")
		d := byKey[k]
		if d == nil {
			d = &day{name: p.Name}
			byKey[k] = d
			order = append(order, k)
		}
		d.best = max(d.best, p.Points)
		d.scores = append(d.scores, fmt.Sprintf("%s %.1f", p.League, p.Points))
	}
	var out []monitor.Event
	for _, k := range order {
		d := byKey[k]
		if d.best < limit {
			continue
		}
		sort.Strings(d.scores)
		out = append(out, monitor.Event{
			Key:      fmt.Sprintf("bigday:%s:%d:%s", season, week, k),
			Title:    fmt.Sprintf("%s: %.1f points", d.name, d.best),
			Body:     strings.Join(d.scores, " · "),
			Priority: monitor.PriorityNormal,
		})
	}
	return out
}

// summary is a chip for each league: the user's score against their
// opponent's, green when winning and red when losing.
func summary(scores []matchup) []monitor.Stat {
	var out []monitor.Stat
	for _, s := range scores {
		st := monitor.Stat{Label: s.League, Value: fmt.Sprintf("%.1f–%.1f", s.Mine, s.Theirs), Detail: "v " + s.Opponent}
		switch {
		case s.Mine > s.Theirs:
			st.Tone = monitor.ToneGood
		case s.Mine < s.Theirs:
			st.Tone = monitor.ToneBad
		}
		out = append(out, st)
	}
	return out
}

// blocks are the week's matchups and the notable plays, newest first.
func blocks(scores []matchup, notes []noted) []monitor.Block {
	table := monitor.Block{
		Type:    monitor.BlockTable,
		Columns: []monitor.Column{{Title: "League"}, {Title: "You", Align: monitor.AlignRight}, {Title: "Them", Align: monitor.AlignRight}, {Title: "Opponent", Fill: true}},
		Rows:    [][]monitor.Cell{},
	}
	for _, s := range scores {
		tone := monitor.ToneNone
		switch {
		case s.Mine > s.Theirs:
			tone = monitor.ToneGood
		case s.Mine < s.Theirs:
			tone = monitor.ToneBad
		}
		table.Rows = append(table.Rows, []monitor.Cell{
			{Text: s.League},
			{Text: fmt.Sprintf("%.1f", s.Mine), Tone: tone},
			{Text: fmt.Sprintf("%.1f", s.Theirs)},
			{Text: s.Opponent, Tone: monitor.ToneMuted},
		})
	}
	out := []monitor.Block{table}

	if len(notes) == 0 {
		return append(out, monitor.Block{Type: monitor.BlockText, Title: "Plays", Text: "Nothing notable from your players yet this week."})
	}
	sorted := append([]noted(nil), notes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.After(sorted[j].At) })
	plays := monitor.Block{Type: monitor.BlockTable, Title: "Plays", Columns: []monitor.Column{{}, {Fill: true}}, Rows: [][]monitor.Cell{}}
	for _, n := range sorted {
		tone := monitor.ToneBad
		if n.Good {
			tone = monitor.ToneGood
		}
		plays.Rows = append(plays.Rows, []monitor.Cell{
			{Text: n.Kind, Tone: tone},
			{Text: n.Text, Detail: strings.Join([]string{n.Game, n.When, strings.Join(n.Leagues, ", ")}, " · ")},
		})
	}
	return append(out, plays)
}

// laterOf is whichever of a and b is later.
func laterOf(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
