package nfl

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture has TB @ DAL final 24–16, CHI @ GB live at 7–3, and PHI @ JAX
// (a London game) kicking off at 13:30 UTC on Sunday.
var now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

func newMonitor(t *testing.T, srv string, extra map[string]any) *Monitor {
	t.Helper()
	cfg := map[string]any{"scoreboardURL": srv, "timezone": "Europe/London"}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestTable(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/scoreboard.json")
	m := newMonitor(t, srv.URL, map[string]any{"teams": []string{"chi"}})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	require.Len(t, res.Blocks, 1)
	b := res.Blocks[0]
	assert.Equal(t, "Week 5", b.Title)
	require.Len(t, b.Rows, 3)
	text := func(r []monitor.Cell) []string {
		var out []string
		for _, c := range r {
			out = append(out, c.Text)
		}
		return out
	}
	assert.Equal(t, []string{"Buccaneers", "24", "Cowboys", "16", "Final"}, text(b.Rows[0]))
	assert.Equal(t, monitor.ToneGood, b.Rows[0][1].Tone, "the winner's score is highlighted")
	assert.Equal(t, []string{"Bears", "7", "Packers", "3", "7:42 - 2nd · 2nd & 6 at GB 41"}, text(b.Rows[1]))
	assert.Equal(t, monitor.ToneAccent, b.Rows[1][0].Tone, "followed teams are highlighted")
	assert.Equal(t, []string{"Eagles", "", "Jaguars", "", "Sun 14:30 NFL Net"}, text(b.Rows[2]), "kick-off in UK time")
}

func TestEventsForFollowedTeams(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/scoreboard.json")

	m := newMonitor(t, srv.URL, map[string]any{"teams": []string{"CHI", "TB"}})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, []string{"final:401872980", "kickoff:401872990", "score:401872990:7-3"}, monitortest.Keys(res.Events))
	assert.Equal(t, "Final: TB 24 – 16 DAL", res.Events[0].Title)
	assert.Equal(t, monitor.PriorityHigh, res.Events[0].Priority)
	assert.Equal(t, "CHI 7 – 3 GB", res.Events[2].Title)

	m = newMonitor(t, srv.URL, map[string]any{"teams": []string{"CHI"}, "scores": false})
	res, err = m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, []string{"kickoff:401872990"}, monitortest.Keys(res.Events))

	m = newMonitor(t, srv.URL, map[string]any{"allFinals": true})
	res, err = m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	require.Equal(t, []string{"final:401872980"}, monitortest.Keys(res.Events))
	assert.Equal(t, monitor.PriorityLow, res.Events[0].Priority, "unfollowed results are low priority")
}

func TestNext(t *testing.T) {
	m := newMonitor(t, "http://unused", map[string]any{"liveInterval": "2m"})
	kickoff := now.Add(90 * time.Minute)
	pre := game{state: "pre", kickoff: kickoff}

	assert.Equal(t, 2*time.Minute, m.next([]game{pre, {state: "in"}}, now), "live game")
	assert.Equal(t, 30*time.Second, newMonitor(t, "http://unused", nil).live, "default live interval")
	assert.Equal(t, 90*time.Minute, m.next([]game{{state: "post"}, pre}, now), "wake for kick-off")
	assert.Equal(t, 2*time.Minute, m.next([]game{pre}, kickoff.Add(time.Minute)), "kick-off passed but ESPN still says pre")
	assert.Zero(t, m.next([]game{{state: "post"}}, now), "nothing coming: keep the interval")
}

func TestSummary(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/scoreboard.json")

	// On a game day: a chip for each of the day's games, in kick-off
	// order, with followed teams' highlighted. Thursday's game isn't today.
	res, err := newMonitor(t, srv.URL, map[string]any{"teams": []string{"CHI"}}).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	assert.Equal(t, []monitor.Stat{
		{Label: "PHI\nJAX", Value: "14:30", Detail: "NFL Net"},
		{Label: "CHI\nGB", Value: "7–3", Detail: "2nd 7:42", Tone: monitor.ToneAccent},
	}, res.Summary, "teams stacked; time and channel, or score and clock")
	res, err = newMonitor(t, srv.URL, nil).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, monitor.ToneWarn, res.Summary[1].Tone, "live, not followed")

	// A live game makes it a game day, whatever the date.
	tuesday := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	res, err = newMonitor(t, srv.URL, nil).Poll(context.Background(), monitortest.Env(tuesday))
	require.NoError(t, err)
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	assert.Equal(t, "CHI\nGB", res.Summary[0].Label)
	assert.True(t, res.Timely, "its age matters while a game's on")

	// Without games today: the followed teams' games this week.
	body, err := os.ReadFile("testdata/scoreboard.json")
	require.NoError(t, err)
	srv.Reply(http.StatusOK, []byte(strings.ReplaceAll(string(body), `"state": "in"`, `"state": "post"`)))
	res, err = newMonitor(t, srv.URL, map[string]any{"teams": []string{"CHI", "DAL", "PHI"}}).Poll(context.Background(), monitortest.Env(tuesday))
	require.NoError(t, err)
	assert.Equal(t, monitor.SummaryTiles, res.SummaryStyle)
	assert.False(t, res.Timely)
	require.Len(t, res.Summary, 3)
	assert.Equal(t, monitor.Stat{Label: "TB @ DAL", Value: "24–16", Detail: "Final", Tone: monitor.ToneBad}, res.Summary[0], "a followed team's loss")
}

func TestClock(t *testing.T) {
	assert.Equal(t, "2nd 7:42", clock("7:42 - 2nd · 2nd & 6 at GB 41"))
	assert.Equal(t, "4th 0:12", clock("0:12 - 4th"))
	assert.Equal(t, "Halftime", clock("Halftime"))
}

func TestTouchdownHeldForTry(t *testing.T) {
	m := newMonitor(t, "http://unused", map[string]any{"teams": []string{"IND"}})
	clock := now
	env := monitortest.Env(now)
	env.Now = func() time.Time { return clock }
	// poll is the score keys a poll would give, the score being away–home.
	poll := func(away, home string) []string {
		g := game{id: "1", state: "in", away: "IND", home: "PIT", awayScore: away, homeScore: home}
		held, err := m.holdTries([]game{g}, env)
		require.NoError(t, err)
		var keys []string
		for _, k := range monitortest.Keys(m.gameEvents(g, held[g.id])) {
			if strings.HasPrefix(k, "score:") {
				keys = append(keys, k)
			}
		}
		return keys
	}

	assert.Equal(t, []string{"score:1:6-7"}, poll("6", "7"), "first sight of a game isn't held")
	assert.Empty(t, poll("6", "13"), "touchdown held for the try")
	clock = clock.Add(30 * time.Second)
	assert.Equal(t, []string{"score:1:6-14"}, poll("6", "14"), "the try is in")

	clock = clock.Add(time.Minute)
	assert.Equal(t, []string{"score:1:9-14"}, poll("9", "14"), "a field goal isn't held")

	assert.Empty(t, poll("15", "14"))
	clock = clock.Add(time.Minute)
	assert.Empty(t, poll("15", "14"), "still waiting for the try")
	clock = clock.Add(time.Minute)
	assert.Equal(t, []string{"score:1:15-14"}, poll("15", "14"), "no try seen in time: the score goes out")
	clock = clock.Add(30 * time.Second)
	assert.Equal(t, []string{"score:1:15-14"}, poll("15", "14"), "and stays out")
}
