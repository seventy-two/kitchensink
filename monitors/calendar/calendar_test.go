package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Monday 12 October 2026, 08:52 BST. The work fixture has a weekday 09:00
// standup (moved to 10:00 on Tuesday, skipped on Wednesday) and a review at
// 15:00; the personal one has bins today, a late film on Tuesday night, a
// call on Wednesday and a weekend away.
var now = time.Date(2026, 10, 12, 8, 52, 0, 0, london)

func newMonitor(t *testing.T, cals ...Calendar) *Monitor {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"calendars": cals})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func feeds(t *testing.T) (work, personal Calendar) {
	w := monitortest.Serve(t, "testdata/work.ics")
	p := monitortest.Serve(t, "testdata/personal.ics")
	return Calendar{Name: "Work", URL: w.URL, Tone: monitor.ToneAccent},
		Calendar{Name: "Personal", URL: p.URL, Tone: monitor.ToneGood}
}

// rows flattens a table to "time | event | calendar" strings.
func rows(b monitor.Block) []string {
	var out []string
	for _, r := range b.Rows {
		s := r[0].Text
		for _, c := range r[1:] {
			s += " | " + c.Text
		}
		out = append(out, s)
	}
	return out
}

func TestDays(t *testing.T) {
	work, personal := feeds(t)
	m := newMonitor(t, work, personal)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	var titles []string
	for _, b := range res.Blocks {
		titles = append(titles, b.Title)
	}
	assert.Equal(t, []string{
		"Today · Monday 12 October", "Tomorrow · Tuesday 13 October", "Wednesday 14 October",
		"Thursday 15 October", "Friday 16 October", "Saturday 17 October", "Sunday 18 October",
	}, titles)

	assert.Equal(t, []string{
		"All day | Bins out | Personal",
		"09:00–09:15 | Standup | Work",
		"15:00–16:00 | Quarterly review · Room 4 | Work",
	}, rows(res.Blocks[0]), "the cancelled meeting and last year's are left out")
	assert.Equal(t, []string{
		"10:00–10:15 | Standup (moved) | Work",
		"22:00–… | Late film | Personal",
	}, rows(res.Blocks[1]))
	assert.Equal(t, []string{
		"…–01:00 | Late film | Personal",
		"18:30–19:00 | Call with Mum | Personal",
	}, rows(res.Blocks[2]), "no standup: it's excluded")
	assert.Equal(t, []string{
		"All day | Weekend in Cardiff | Personal",
		"09:00–09:15 | Standup | Work",
	}, rows(res.Blocks[4]))
	assert.Equal(t, []string{"All day | Weekend in Cardiff | Personal"}, rows(res.Blocks[6]))

	standup := res.Blocks[0].Rows[1]
	assert.Equal(t, "https://teams.microsoft.com/l/meetup-join/19%3ameeting_abc%40thread.v2/0?context=x", standup[1].URL)
	assert.Equal(t, monitor.ToneAccent, standup[2].Tone, "the calendar's own tone")
	assert.Equal(t, "https://meet.google.com/abc-defg-hij", res.Blocks[2].Rows[1][1].URL)
}

func TestTones(t *testing.T) {
	work, _ := feeds(t)
	m := newMonitor(t, work)
	at := time.Date(2026, 10, 12, 15, 30, 0, 0, london)
	res, err := m.Poll(context.Background(), monitortest.Env(at))
	require.NoError(t, err)
	today := res.Blocks[0]
	require.Len(t, today.Columns, 2, "no calendar column for a single calendar")
	assert.Equal(t, monitor.ToneMuted, today.Rows[0][1].Tone, "the standup is over")
	assert.Equal(t, monitor.ToneAccent, today.Rows[1][1].Tone, "the review is on now")
}

func TestReminders(t *testing.T) {
	work, personal := feeds(t)
	m := newMonitor(t, work, personal)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	require.Len(t, res.Events, 1)
	e := res.Events[0]
	assert.Equal(t, "Standup at 09:00", e.Title)
	assert.Equal(t, "In 8 minutes · Work", e.Body)
	assert.Equal(t, "remind:Work:standup-1:2026-10-12T08:00:00Z", e.Key)
	assert.Contains(t, e.URL, "teams.microsoft.com")

	// The next poll is when the review's reminder is due, at 14:50.
	assert.Equal(t, 5*time.Hour+58*time.Minute, res.Next)
}

func TestRelevant(t *testing.T) {
	work, _ := feeds(t)
	m := newMonitor(t, work)
	at := func(h, min int) time.Time { return time.Date(2026, 10, 12, h, min, 0, 0, london) }
	for _, c := range []struct {
		now  time.Time
		want monitor.Window
	}{
		// From an hour before the standup until it ends, including during it.
		{at(8, 52), monitor.Window{From: at(8, 0), Until: at(9, 15)}},
		{at(9, 5), monitor.Window{From: at(8, 0), Until: at(9, 15)}},
		// Then the review.
		{at(9, 15), monitor.Window{From: at(14, 0), Until: at(16, 0)}},
	} {
		res, err := m.Poll(context.Background(), monitortest.Env(c.now))
		require.NoError(t, err)
		assert.True(t, c.want.From.Equal(res.Relevant.From), "%v: from %v", c.now, res.Relevant.From)
		assert.True(t, c.want.Until.Equal(res.Relevant.Until), "%v: until %v", c.now, res.Relevant.Until)
	}
}

func TestNextAtMidnight(t *testing.T) {
	work, _ := feeds(t)
	m := newMonitor(t, work)
	at := time.Date(2026, 10, 12, 20, 0, 0, 0, london)
	res, err := m.Poll(context.Background(), monitortest.Env(at))
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Equal(t, 4*time.Hour, res.Next, "midnight comes before tomorrow's reminders")
}

func TestRemindersOff(t *testing.T) {
	work, _ := feeds(t)
	raw, _ := json.Marshal(map[string]any{"calendars": []Calendar{work}, "remindMinutes": 0})
	m, err := New(raw)
	require.NoError(t, err)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Empty(t, res.Events)
}

func TestEmptyToday(t *testing.T) {
	work, _ := feeds(t)
	m := newMonitor(t, work)
	sat := time.Date(2026, 10, 17, 9, 0, 0, 0, london)
	res, err := m.Poll(context.Background(), monitortest.Env(sat))
	require.NoError(t, err)
	assert.Equal(t, monitor.BlockText, res.Blocks[0].Type)
	assert.Equal(t, "Nothing on today.", res.Blocks[0].Text)
	assert.Equal(t, "Monday 19 October", res.Blocks[1].Title)
}

func TestFailures(t *testing.T) {
	_, personal := feeds(t)
	gone := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gone.Close)
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	// One feed failing still shows the others.
	m := newMonitor(t, Calendar{Name: "Work", URL: gone.URL + "/private-secret/basic.ics"}, personal)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, "Couldn't load Work: feed returned 404 Not Found", res.Blocks[0].Text)
	assert.Equal(t, "Today · Monday 12 October", res.Blocks[1].Title)

	// All failing is an error, which never shows the secret URL.
	m = newMonitor(t, Calendar{Name: "Work", URL: closed.URL + "/private-secret/basic.ics"})
	_, err = m.Poll(context.Background(), monitortest.Env(now))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Work: ")
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), closed.URL)
}

func TestConfig(t *testing.T) {
	for name, cfg := range map[string]string{
		"no calendars": `{}`,
		"no url":       `{"calendars": [{"name": "Work"}]}`,
		"bad tone":     `{"calendars": [{"url": "https://example.com", "tone": "pink"}]}`,
		"bad zone":     `{"calendars": [{"url": "https://example.com"}], "timezone": "Mars/Base"}`,
		"unknown":      `{"calendars": [{"url": "https://example.com"}], "colour": "red"}`,
	} {
		_, err := New(json.RawMessage(cfg))
		assert.Error(t, err, name)
	}
}

func TestSummary(t *testing.T) {
	work, personal := feeds(t)
	m := newMonitor(t, work, personal)
	brief := func(at time.Time) []string {
		res, err := m.Poll(context.Background(), monitortest.Env(at))
		require.NoError(t, err)
		var out []string
		for _, s := range res.Summary {
			out = append(out, s.Label+" | "+s.Value+" | "+s.Detail)
		}
		return out
	}
	// The next event, not the rest of the week; today's all-day "Bins out"
	// waits for a quiet moment.
	assert.Equal(t, []string{"in 8 min | Standup | Work"}, brief(now))
	// What's on now leads.
	assert.Equal(t, []string{
		"Now, until 16:00 | Quarterly review | Work",
		"Tomorrow 10:00 | Standup (moved) | Work",
	}, brief(time.Date(2026, 10, 12, 15, 30, 0, 0, london)))
	assert.Equal(t, []string{"Tomorrow 09:00 | Standup | Work"}, brief(time.Date(2026, 10, 14, 20, 0, 0, 0, london)))
}

func TestTableLayout(t *testing.T) {
	work, personal := feeds(t)
	m := newMonitor(t, work, personal)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	cols := res.Blocks[0].Columns
	require.Len(t, cols, 3)
	for _, c := range cols {
		assert.Empty(t, c.Title, "no repeated headings")
	}
	assert.True(t, cols[1].Fill, "the event takes the spare width")
}
