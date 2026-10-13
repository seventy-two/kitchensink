package fantasy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// getJSON fetches url into v, with any extra headers.
func getJSON(ctx context.Context, client *http.Client, url string, v any, headers ...string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", trimQuery(url), resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("decoding %s: %w", trimQuery(url), err)
	}
	return nil
}

// trimQuery drops a URL's query, so errors don't repeat long parameters.
func trimQuery(u string) string {
	s, _, _ := strings.Cut(u, "?")
	return s
}

// league is one of the user's leagues, with what's needed to read its
// matchup each poll.
type league struct {
	Kind  string `json:"kind"` // "sleeper" or "espn"
	ID    string `json:"id"`
	Name  string `json:"name"`  // short name
	Index int    `json:"index"` // which ESPN config, for ESPN leagues
	// Sleeper: the user's roster, and owners' names by roster ID.
	RosterID int            `json:"rosterID,omitempty"`
	Owners   map[int]string `json:"owners,omitempty"`
}

// roster is everything the user has, refreshed now and then.
type roster struct {
	Season string `json:"season"`
	Week   int    `json:"week"`
	// Shown is the week whose matchups to show: Sleeper's display week,
	// which stays on the week just played until midweek, so Monday night's
	// results are still there on Tuesday morning in the UK.
	Shown    int                 `json:"shown,omitempty"`
	Leagues  []league            `json:"leagues"`
	Players  map[string]owned    `json:"players"`  // by ESPN athlete ID
	Defences map[string][]string `json:"defences"` // league names, by team abbreviation
}

// sleeperState is the NFL's season and week, from Sleeper, and the week
// its own app shows.
type sleeperState struct {
	Season      string `json:"season"`
	Week        int    `json:"week"`
	DisplayWeek int    `json:"display_week"`
}

// shownWeek is the week whose matchups to show (see Shown).
func (r *roster) shownWeek() int {
	if r.Shown > 0 && r.Shown <= r.Week {
		return r.Shown
	}
	return r.Week
}

// espnTeam is a pro team from ESPN's team list.
type espnTeam struct {
	ID   string
	Abbr string
}

// loadRoster reads the user's leagues and players from Sleeper and ESPN,
// and finds each player's ESPN athlete ID.
func (m *Monitor) loadRoster(ctx context.Context, client *http.Client) (*roster, error) {
	var state sleeperState
	if err := getJSON(ctx, client, m.cfg.SleeperURL+"/state/nfl", &state); err != nil {
		return nil, err
	}
	r := &roster{Season: state.Season, Week: state.Week, Shown: state.DisplayWeek, Players: map[string]owned{}, Defences: map[string][]string{}}
	if r.Shown <= 0 || r.Shown > r.Week {
		r.Shown = r.Week
	}
	teams, err := m.espnTeams(ctx, client)
	if err != nil {
		return nil, err
	}
	abbrByID := map[string]string{}
	for _, t := range teams {
		abbrByID[t.ID] = t.Abbr
	}
	own := func(id, name, team, leagueName string) {
		o := r.Players[id]
		o.Name, o.Team = name, team
		o.Leagues = union(o.Leagues, []string{leagueName})
		r.Players[id] = o
	}

	if m.cfg.SleeperUser != "" {
		if err := m.loadSleeper(ctx, client, r, teams, own); err != nil {
			return nil, fmt.Errorf("sleeper: %w", err)
		}
	}
	for i, l := range m.cfg.ESPN {
		var data espnLeague
		if err := getJSON(ctx, client, m.espnLeagueURL(r.Season, l.LeagueID, "mTeam", "mRoster", "mSettings"), &data, "Cookie", l.cookie()); err != nil {
			return nil, fmt.Errorf("espn league %s: %w", l.LeagueID, err)
		}
		name := m.shortName(l.LeagueID, data.Settings.Name)
		r.Leagues = append(r.Leagues, league{Kind: "espn", ID: l.LeagueID, Name: name, Index: i})
		for _, t := range data.Teams {
			if !t.ownedBy(l.SWID) {
				continue
			}
			for _, e := range t.Roster.Entries {
				p := e.Entry.Player
				team := abbrByID[strconv.Itoa(p.ProTeamID)]
				if p.ID < 0 || p.DefaultPositionID == 16 { // a team defence
					r.Defences[team] = union(r.Defences[team], []string{name})
					continue
				}
				own(strconv.Itoa(p.ID), p.FullName, team, name)
			}
		}
	}
	return r, nil
}

// sleeperPlayer is an entry in Sleeper's player list.
type sleeperPlayer struct {
	FullName string          `json:"full_name"`
	Team     string          `json:"team"`
	Position string          `json:"position"`
	ESPNID   json.RawMessage `json:"espn_id"` // a number, a string or null
}

// espnID is the player's ESPN athlete ID, if Sleeper has one.
func (p sleeperPlayer) espnID() string {
	id := strings.Trim(string(p.ESPNID), `"`)
	if id == "null" || id == "0" {
		return ""
	}
	return id
}

func (m *Monitor) loadSleeper(ctx context.Context, client *http.Client, r *roster, teams []espnTeam, own func(id, name, team, league string)) error {
	var user struct {
		UserID string `json:"user_id"`
	}
	if err := getJSON(ctx, client, m.cfg.SleeperURL+"/user/"+m.cfg.SleeperUser, &user); err != nil {
		return err
	}
	if user.UserID == "" {
		return fmt.Errorf("no user %q", m.cfg.SleeperUser)
	}
	var leagues []struct {
		ID   string `json:"league_id"`
		Name string `json:"name"`
	}
	if err := getJSON(ctx, client, m.cfg.SleeperURL+"/user/"+user.UserID+"/leagues/nfl/"+r.Season, &leagues); err != nil {
		return err
	}
	// Sleeper's full player list is large; it's only read when rosters
	// are, a few times a day.
	var players map[string]sleeperPlayer
	if err := getJSON(ctx, client, m.cfg.SleeperURL+"/players/nfl", &players); err != nil {
		return err
	}
	espnIDs, err := m.espnRosters(ctx, client, teams)
	if err != nil {
		return err
	}
	for _, l := range leagues {
		var rosters []struct {
			RosterID int      `json:"roster_id"`
			OwnerID  string   `json:"owner_id"`
			CoOwners []string `json:"co_owners"`
			Players  []string `json:"players"`
		}
		if err := getJSON(ctx, client, m.cfg.SleeperURL+"/league/"+l.ID+"/rosters", &rosters); err != nil {
			return err
		}
		var users []struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
			Metadata    struct {
				TeamName string `json:"team_name"`
			} `json:"metadata"`
		}
		if err := getJSON(ctx, client, m.cfg.SleeperURL+"/league/"+l.ID+"/users", &users); err != nil {
			return err
		}
		names := map[string]string{}
		for _, u := range users {
			names[u.UserID] = u.DisplayName
			if u.Metadata.TeamName != "" {
				names[u.UserID] = u.Metadata.TeamName
			}
		}
		lg := league{Kind: "sleeper", ID: l.ID, Name: m.shortName(l.ID, l.Name), Owners: map[int]string{}}
		for _, ro := range rosters {
			lg.Owners[ro.RosterID] = names[ro.OwnerID]
			if ro.OwnerID != user.UserID && !contains(ro.CoOwners, user.UserID) {
				continue
			}
			lg.RosterID = ro.RosterID
			for _, pid := range ro.Players {
				p, ok := players[pid]
				if !ok {
					continue
				}
				team := sleeperTeam(p.Team)
				if p.Position == "DEF" {
					r.Defences[sleeperTeam(pid)] = union(r.Defences[sleeperTeam(pid)], []string{lg.Name})
					continue
				}
				// Sleeper's own ESPN ID where it has one, else a match on
				// name and team against ESPN's rosters.
				id := p.espnID()
				if id == "" {
					id = espnIDs[nameKey(p.FullName, team)]
				}
				if id == "" {
					continue // not on an ESPN roster, e.g. a free agent
				}
				own(id, p.FullName, team, lg.Name)
			}
		}
		r.Leagues = append(r.Leagues, lg)
	}
	return nil
}

// espnTeams lists the NFL's teams from ESPN.
func (m *Monitor) espnTeams(ctx context.Context, client *http.Client) ([]espnTeam, error) {
	var data struct {
		Sports []struct {
			Leagues []struct {
				Teams []struct {
					Team struct {
						ID   string `json:"id"`
						Abbr string `json:"abbreviation"`
					} `json:"team"`
				} `json:"teams"`
			} `json:"leagues"`
		} `json:"sports"`
	}
	if err := getJSON(ctx, client, m.cfg.SiteURL+"/teams", &data); err != nil {
		return nil, err
	}
	var out []espnTeam
	for _, s := range data.Sports {
		for _, l := range s.Leagues {
			for _, t := range l.Teams {
				out = append(out, espnTeam{t.Team.ID, t.Team.Abbr})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no teams from ESPN")
	}
	return out, nil
}

// espnRosters maps players' names and teams to their ESPN athlete IDs.
func (m *Monitor) espnRosters(ctx context.Context, client *http.Client, teams []espnTeam) (map[string]string, error) {
	out := map[string]string{}
	for _, t := range teams {
		var data struct {
			Athletes []struct {
				Items []struct {
					ID       string `json:"id"`
					FullName string `json:"fullName"`
				} `json:"items"`
			} `json:"athletes"`
		}
		if err := getJSON(ctx, client, m.cfg.SiteURL+"/teams/"+t.ID+"/roster", &data); err != nil {
			return nil, err
		}
		for _, g := range data.Athletes {
			for _, a := range g.Items {
				out[nameKey(a.FullName, t.Abbr)] = a.ID
			}
		}
	}
	return out, nil
}

// sleeperTeam turns Sleeper's team abbreviations into ESPN's.
func sleeperTeam(t string) string {
	switch t {
	case "WAS":
		return "WSH"
	case "JAC":
		return "JAX"
	case "LA":
		return "LAR"
	}
	return t
}

var suffix = regexp.MustCompile(`\b(jr|sr|ii|iii|iv|v)\b`)

// plain drops accents and the punctuation found in players' names.
var plain = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ú", "u",
	"ù", "u", "û", "u", "ü", "u", "ñ", "n", "ç", "c", ".", "", "'", "", "’", "", "-", "",
)

// nameKey makes a player's name and team comparable across sites:
// accents, punctuation and suffixes such as "Jr." are dropped.
func nameKey(name, team string) string {
	n := plain.Replace(strings.ToLower(name))
	return strings.Join(strings.Fields(suffix.ReplaceAllString(n, "")), " ") + "|" + team
}

func contains(xs []string, x string) bool { return slices.Contains(xs, x) }

// shortName is a league's name as configured, or as the site has it.
func (m *Monitor) shortName(id, name string) string {
	if n, ok := m.cfg.Names[id]; ok {
		return n
	}
	return name
}

// espnLeague is the part of an ESPN fantasy league the monitor uses.
type espnLeague struct {
	Settings struct {
		Name string `json:"name"`
	} `json:"settings"`
	Status struct {
		CurrentMatchupPeriod int `json:"currentMatchupPeriod"`
	} `json:"status"`
	Teams    []espnFantasyTeam `json:"teams"`
	Schedule []struct {
		MatchupPeriodID int      `json:"matchupPeriodId"`
		Home            espnSide `json:"home"`
		Away            espnSide `json:"away"`
	} `json:"schedule"`
}

type espnFantasyTeam struct {
	ID     int      `json:"id"`
	Name   string   `json:"name"`
	Owners []string `json:"owners"`
	Roster struct {
		Entries []struct {
			Entry struct {
				Player struct {
					ID                int    `json:"id"`
					FullName          string `json:"fullName"`
					ProTeamID         int    `json:"proTeamId"`
					DefaultPositionID int    `json:"defaultPositionId"`
				} `json:"player"`
			} `json:"playerPoolEntry"`
		} `json:"entries"`
	} `json:"roster"`
}

func (t espnFantasyTeam) ownedBy(swid string) bool { return contains(t.Owners, swid) }

type espnSide struct {
	TeamID     int      `json:"teamId"`
	Points     float64  `json:"totalPoints"`
	PointsLive *float64 `json:"totalPointsLive"`
}

func (s espnSide) points() float64 {
	if s.PointsLive != nil {
		return *s.PointsLive
	}
	return s.Points
}

func (m *Monitor) espnLeagueURL(season, id string, views ...string) string {
	u := m.cfg.ESPNURL + "/seasons/" + season + "/segments/0/leagues/" + id
	for i, v := range views {
		sep := "&"
		if i == 0 {
			sep = "?"
		}
		u += sep + "view=" + v
	}
	return u
}

// matchup is the user's game in the shown week in one league.
type matchup struct {
	League   string
	Mine     float64
	Theirs   float64
	Opponent string
}

// matchups reads the live score of the user's matchup in each league.
// A league that can't be read is left out.
func (m *Monitor) matchups(ctx context.Context, client *http.Client, r *roster, logf func(string, ...any)) []matchup {
	var out []matchup
	for _, l := range r.Leagues {
		switch l.Kind {
		case "sleeper":
			var ms []struct {
				RosterID  int     `json:"roster_id"`
				MatchupID int     `json:"matchup_id"`
				Points    float64 `json:"points"`
			}
			if err := getJSON(ctx, client, fmt.Sprintf("%s/league/%s/matchups/%d", m.cfg.SleeperURL, l.ID, r.shownWeek()), &ms); err != nil {
				logf("reading matchups", "league", l.Name, "err", err)
				continue
			}
			mu := matchup{League: l.Name}
			mid := -1
			for _, x := range ms {
				if x.RosterID == l.RosterID {
					mu.Mine, mid = x.Points, x.MatchupID
				}
			}
			for _, x := range ms {
				if mid > 0 && x.MatchupID == mid && x.RosterID != l.RosterID {
					mu.Theirs, mu.Opponent = x.Points, l.Owners[x.RosterID]
				}
			}
			if mid > 0 {
				out = append(out, mu)
			}
		case "espn":
			cfg := m.cfg.ESPN[l.Index]
			var data espnLeague
			if err := getJSON(ctx, client, m.espnLeagueURL(r.Season, l.ID, "mMatchupScore", "mTeam"), &data, "Cookie", cfg.cookie()); err != nil {
				logf("reading matchups", "league", l.Name, "err", err)
				continue
			}
			mine, names := -1, map[int]string{}
			for _, t := range data.Teams {
				names[t.ID] = t.Name
				if t.ownedBy(cfg.SWID) {
					mine = t.ID
				}
			}
			// ESPN moves on to the new week's matchups when the NFL does;
			// step back as far as the shown week is behind.
			period := data.Status.CurrentMatchupPeriod - (r.Week - r.shownWeek())
			for _, s := range data.Schedule {
				if s.MatchupPeriodID != period {
					continue
				}
				me, them := s.Home, s.Away
				if s.Away.TeamID == mine {
					me, them = s.Away, s.Home
				} else if s.Home.TeamID != mine {
					continue
				}
				out = append(out, matchup{League: l.Name, Mine: me.points(), Theirs: them.points(), Opponent: names[them.TeamID]})
			}
		}
	}
	return out
}
