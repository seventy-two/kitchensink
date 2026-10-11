package fantasy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Thursday's TB @ DAL game, as testdata/plays.json has it, is live, and
// first seen just after kick-off. The
// user has, on Sleeper ("Test League"), Javonte Williams (DAL; Sleeper has
// no ESPN ID for him, so he's matched by name), Bucky Irving (TB) and the
// TB defence; and on ESPN ("Work"), Dak Prescott and Brandon Aubrey (DAL)
// and the DAL defence.
var now = time.Date(2026, 10, 9, 0, 20, 0, 0, time.UTC)

type fakeAPIs struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	state    string // the game's state on the scoreboard
	plays    []byte
	cookie   string
}

func newFakeAPIs(t *testing.T) *fakeAPIs {
	plays, err := os.ReadFile("testdata/plays.json")
	require.NoError(t, err)
	f := &fakeAPIs{state: "in", plays: plays}
	js := func(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }
	mux := http.NewServeMux()
	// Sleeper.
	mux.HandleFunc("/sleeper/state/nfl", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"season": "2026", "week": 5})
	})
	mux.HandleFunc("/sleeper/user/tester", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"user_id": "U1"})
	})
	mux.HandleFunc("/sleeper/user/U1/leagues/nfl/2026", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{map[string]any{"league_id": "L1", "name": "Test League"}})
	})
	mux.HandleFunc("/sleeper/players/nfl", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{
			"jw": map[string]any{"full_name": "Javonte Williams", "team": "DAL", "position": "RB"},
			"bi": map[string]any{"full_name": "Bucky Irving", "team": "TB", "position": "RB", "espn_id": 4596448},
			"TB": map[string]any{"team": "TB", "position": "DEF"},
		})
	})
	mux.HandleFunc("/sleeper/league/L1/rosters", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{
			map[string]any{"roster_id": 1, "owner_id": "U1", "players": []string{"jw", "bi", "TB"}},
			map[string]any{"roster_id": 2, "owner_id": "U2", "players": []string{}},
		})
	})
	mux.HandleFunc("/sleeper/league/L1/users", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{
			map[string]any{"user_id": "U1", "display_name": "tester"},
			map[string]any{"user_id": "U2", "display_name": "rival", "metadata": map[string]any{"team_name": "Rival FC"}},
		})
	})
	mux.HandleFunc("/sleeper/league/L1/matchups/5", func(w http.ResponseWriter, r *http.Request) {
		js(w, []any{
			map[string]any{"roster_id": 1, "matchup_id": 1, "points": 32.9},
			map[string]any{"roster_id": 2, "matchup_id": 1, "points": 14.64},
		})
	})
	// ESPN fantasy.
	mux.HandleFunc("/espn/seasons/2026/segments/0/leagues/E1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.cookie = r.Header.Get("Cookie")
		f.mu.Unlock()
		if !strings.Contains(r.Header.Get("Cookie"), "SWID={ME}") {
			http.Error(w, "private", http.StatusUnauthorized)
			return
		}
		live := 61.5
		js(w, map[string]any{
			"settings": map[string]any{"name": "Office League 2026"},
			"status":   map[string]any{"currentMatchupPeriod": 5},
			"teams": []any{
				map[string]any{"id": 10, "name": "Mine", "owners": []string{"{ME}"}, "roster": map[string]any{"entries": []any{
					entry(2577417, "Dak Prescott", 6, 1), entry(3953687, "Brandon Aubrey", 6, 5), entry(-16006, "Cowboys D/ST", 6, 16),
				}}},
				map[string]any{"id": 1, "name": "Their Team", "owners": []string{"{THEM}"}},
			},
			"schedule": []any{
				map[string]any{"matchupPeriodId": 4, "home": map[string]any{"teamId": 10, "totalPoints": 99}, "away": map[string]any{"teamId": 1, "totalPoints": 1}},
				map[string]any{"matchupPeriodId": 5, "home": map[string]any{"teamId": 1, "totalPoints": 0, "totalPointsLive": 70.2}, "away": map[string]any{"teamId": 10, "totalPoints": 0, "totalPointsLive": live}},
			},
		})
	})
	// ESPN's sports data.
	mux.HandleFunc("/site/teams", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"sports": []any{map[string]any{"leagues": []any{map[string]any{"teams": []any{
			map[string]any{"team": map[string]any{"id": "6", "abbreviation": "DAL"}},
			map[string]any{"team": map[string]any{"id": "27", "abbreviation": "TB"}},
		}}}}}})
	})
	mux.HandleFunc("/site/teams/6/roster", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"athletes": []any{map[string]any{"items": []any{map[string]any{"id": "4361579", "fullName": "Javonte Williams"}}}}})
	})
	mux.HandleFunc("/site/teams/27/roster", func(w http.ResponseWriter, r *http.Request) {
		js(w, map[string]any{"athletes": []any{map[string]any{"items": []any{map[string]any{"id": "4596448", "fullName": "Bucky Irving"}}}}})
	})
	mux.HandleFunc("/site/scoreboard", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		state := f.state
		f.mu.Unlock()
		js(w, map[string]any{"events": []any{
			fakeGame("401872980", "TB @ DAL", "2026-10-09T00:15Z", state, "6", "DAL", "27", "TB"),
			fakeGame("401872999", "PHI @ JAX", "2026-10-11T13:30Z", "pre", "30", "JAX", "21", "PHI"),
		}})
	})
	mux.HandleFunc("/core/events/401872980/competitions/401872980/plays", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Write(f.plays)
	})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.Path)
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

func entry(id int, name string, team, pos int) map[string]any {
	return map[string]any{"playerPoolEntry": map[string]any{"player": map[string]any{
		"id": id, "fullName": name, "proTeamId": team, "defaultPositionId": pos,
	}}}
}

func fakeGame(id, name, date, state, homeID, home, awayID, away string) map[string]any {
	return map[string]any{"id": id, "shortName": name, "date": date, "competitions": []any{map[string]any{
		"status": map[string]any{"type": map[string]any{"state": state}},
		"competitors": []any{
			map[string]any{"homeAway": "home", "team": map[string]any{"id": homeID, "abbreviation": home}},
			map[string]any{"homeAway": "away", "team": map[string]any{"id": awayID, "abbreviation": away}},
		},
	}}}
}

func (f *fakeAPIs) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	return n
}

func newMonitor(t *testing.T, f *fakeAPIs) *Monitor {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"sleeperUser": "tester",
		"espn":        []any{map[string]any{"leagueId": "E1", "s2": "s3cret", "swid": "{ME}"}},
		"names":       map[string]string{"E1": "Work"},
		"sleeperURL":  f.URL + "/sleeper",
		"espnURL":     f.URL + "/espn",
		"siteURL":     f.URL + "/site",
		"coreURL":     f.URL + "/core",
	})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestPlays(t *testing.T) {
	f := newFakeAPIs(t)
	m := newMonitor(t, f)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	var got []string
	for _, e := range res.Events {
		got = append(got, e.Title+" | "+e.Body)
	}
	assert.Equal(t, []string{
		"Big play: Javonte Williams 21 Yd Rush | TB @ DAL · Q1 13:19 · Test League",
		"Big play: Dak Prescott Pass Complete for 21 Yds to George Pickens | TB @ DAL · Q1 12:41 · Work",
		"TD: Javonte Williams 1 Yd Rush (Brandon Aubrey Kick) | TB @ DAL · Q1 10:51 · Test League, Work",
		"TD: Bucky Irving 5 Yd pass from Jalon Daniels (Chase McLaughlin Kick) | TB @ DAL · Q2 14:53 · Test League",
		"Big play: Dak Prescott Pass Complete for 48 Yds to Ryan Flournoy | TB @ DAL · Q2 1:52 · Work",
		"Big play: Bucky Irving 72 Yd Rush | TB @ DAL · Q3 14:54 · Test League",
		"D/ST takeaway: Yaya Diaby 1 Yd Interception Return | TB @ DAL · Q3 10:51 · Test League",
		"INT thrown: Yaya Diaby 1 Yd Interception Return | TB @ DAL · Q3 10:51 · Work",
		"D/ST sack: Jalon Daniels Sacked by Malachi Lawrence For 11 Yd Loss | TB @ DAL · Q3 7:24 · Work",
		"TD: George Pickens 18 Yd pass from Dak Prescott (Two-Point Pass Conversion Failed) | TB @ DAL · Q4 5:26 · Work",
		"D/ST takeaway: Alex Anzalone Interception Return, No Gain | TB @ DAL · Q4 1:22 · Test League",
		"INT thrown: Alex Anzalone Interception Return, No Gain | TB @ DAL · Q4 1:22 · Work",
	}, got, "kickoffs, short gains and a 41-yard field goal don't count; an interception counts both ways")
	for _, e := range res.Events {
		assert.False(t, e.Quiet, "a game with its plays read from the start notifies")
	}
	assert.Equal(t, monitor.PriorityHigh, res.Events[2].Priority, "touchdowns are high")
	assert.Equal(t, monitor.PriorityLow, res.Events[8].Priority, "sacks are low")
	assert.Equal(t, 30*time.Second, res.Next, "checks often while the game's on")

	// The panel: each league's score, and the plays, newest first.
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	assert.Equal(t, []monitor.Stat{
		{Label: "Test League", Value: "32.9–14.6", Detail: "v Rival FC", Tone: monitor.ToneGood},
		{Label: "Work", Value: "61.5–70.2", Detail: "v Their Team", Tone: monitor.ToneBad},
	}, res.Summary)
	require.Len(t, res.Blocks, 2)
	plays := res.Blocks[1]
	assert.Len(t, plays.Rows, 12)
	assert.Equal(t, "Big play", plays.Rows[0][0].Text)
	assert.Equal(t, monitor.ToneGood, plays.Rows[0][0].Tone)
	assert.Equal(t, "TB @ DAL · Q1 13:19 · Test League", plays.Rows[0][1].Detail)
	assert.Contains(t, f.cookie, "espn_s2=s3cret", "the ESPN league is read with the cookies")

	// The next check reads only new plays: there are none.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 1, f.count("/sleeper/players/nfl"), "rosters are kept between checks")
}

func TestJoiningLate(t *testing.T) {
	// First seen well after kick-off, a game's plays so far are logged
	// quietly rather than notified all at once.
	f := newFakeAPIs(t)
	res, err := newMonitor(t, f).Poll(context.Background(), monitortest.Env(now.Add(time.Hour)))
	require.NoError(t, err)
	require.Len(t, res.Events, 12)
	for _, e := range res.Events {
		assert.True(t, e.Quiet)
	}
	assert.Len(t, res.Blocks[1].Rows, 12, "but they're still on the panel")
}

func TestBetweenGames(t *testing.T) {
	f := newFakeAPIs(t)
	f.state = "pre"
	m := newMonitor(t, f)
	res, err := m.Poll(context.Background(), monitortest.Env(time.Date(2026, 10, 8, 20, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 4*time.Hour+15*time.Minute, res.Next, "wakes for kick-off")
	assert.Zero(t, f.count("/core/"), "no plays read before kick-off")
	assert.Equal(t, "Nothing notable from your players yet this week.", res.Blocks[1].Text)
}

func TestNameKey(t *testing.T) {
	assert.Equal(t, nameKey("Javonte Williams", "DAL"), nameKey("Javonte Williams", "DAL"))
	assert.Equal(t, "marvin harrison|ARI", nameKey("Marvin Harrison Jr.", "ARI"))
	assert.Equal(t, "4596448", sleeperPlayer{ESPNID: json.RawMessage(`4596448`)}.espnID(), "a number, as Sleeper sends it")
	assert.Equal(t, "4596448", sleeperPlayer{ESPNID: json.RawMessage(`"4596448"`)}.espnID())
	assert.Empty(t, sleeperPlayer{ESPNID: json.RawMessage(`null`)}.espnID())
	assert.Equal(t, "amonra st brown|DET", nameKey("Amon-Ra St. Brown", "DET"))
	assert.Equal(t, "dandre swift|CHI", nameKey("D'Andre Swift", "CHI"))
	assert.Equal(t, "jose ramirez|SEA", nameKey("José Ramírez", "SEA"))
	assert.Equal(t, "WSH", sleeperTeam("WAS"))
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{}`))
	assert.Error(t, err)
	_, err = New(json.RawMessage(`{"espn": [{"leagueId": "1"}]}`))
	assert.Error(t, err, "needs the swid")
}
