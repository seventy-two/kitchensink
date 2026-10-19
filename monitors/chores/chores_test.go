package chores

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tuesday 13 October 2026, 09:00 in London.
var london, _ = time.LoadLocation("Europe/London")
var now = time.Date(2026, 10, 13, 9, 0, 0, 0, london)

const laundry = `{
	"perDay": {"washer": 1},
	"chores": [
		{"name": "Whites wash", "every": 7, "uses": "washer"},
		{"name": "Black tee wash", "every": 7, "uses": "washer"},
		{"name": "Towel wash", "every": 7, "uses": "washer"},
		{"name": "Bedsheet wash", "every": 14, "flex": 3, "days": ["Sat", "Sun"], "uses": "washer"},
		{"name": "Waitrose order", "every": 7, "flex": 0}
	]
}`

func newMonitor(t *testing.T, cfg string) *Monitor {
	t.Helper()
	m, err := New(json.RawMessage(cfg))
	require.NoError(t, err)
	return m
}

func at(env monitor.Env, t time.Time) monitor.Env {
	env.Now = func() time.Time { return t }
	return env
}

// lines is each checklist's items as "[x] title" lines.
func lines(t *testing.T, res monitor.Result) (today, later []string) {
	t.Helper()
	require.Len(t, res.Blocks, 2)
	f := func(b monitor.Block) []string {
		assert.Equal(t, monitor.BlockChecklist, b.Type)
		assert.True(t, b.NoDelete && b.Movable)
		assert.Equal(t, "Put off a day", b.DownHint)
		assert.Empty(t, b.AddHint)
		var out []string
		for _, it := range b.Items {
			mark := "[ ] "
			if it.Done {
				mark = "[x] "
			}
			out = append(out, mark+it.Title)
		}
		return out
	}
	return f(res.Blocks[0]), f(res.Blocks[1])
}

func poll(t *testing.T, m *Monitor, env monitor.Env) monitor.Result {
	t.Helper()
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	return res
}

func act(t *testing.T, m *Monitor, env monitor.Env, typ, id string) {
	t.Helper()
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: typ, ID: id}))
}

func TestStartsStaggered(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	today, later := lines(t, poll(t, m, env))
	// Nothing's been done, so everything's due today, but the washer
	// takes one a day: the rest follow on, the sheets at the weekend.
	assert.Equal(t, []string{"[ ] Whites wash", "[ ] Waitrose order"}, today)
	assert.Equal(t, []string{
		"[ ] Black tee wash · tomorrow",
		"[ ] Towel wash · Thu",
		"[ ] Bedsheet wash · Sat",
	}, later)
}

func TestDoneAndDueAgain(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	act(t, m, env, "toggle", "whites-wash")
	act(t, m, env, "toggle", "towel-wash") // done early, so it takes today's washer slot back from nobody
	res := poll(t, m, env)
	today, later := lines(t, res)
	assert.Equal(t, []string{"[ ] Waitrose order", "[x] Whites wash", "[x] Towel wash"}, today)
	assert.Equal(t, []string{"[ ] Black tee wash · tomorrow", "[ ] Bedsheet wash · Sat"}, later)
	assert.Equal(t, []monitor.Stat{
		{Label: "Today", Value: "Waitrose order", Tone: monitor.ToneAccent},
		{Label: "Done", Value: "Whites wash", Tone: monitor.ToneGood},
		{Label: "Done", Value: "Towel wash", Tone: monitor.ToneGood},
	}, res.Summary, "only today's chores, done or not")
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)

	// Unticking undoes it.
	act(t, m, env, "toggle", "towel-wash")
	today, _ = lines(t, poll(t, m, env))
	assert.Equal(t, []string{"[ ] Waitrose order", "[x] Whites wash"}, today)
	act(t, m, env, "toggle", "towel-wash")
	act(t, m, env, "toggle", "waitrose-order")
	res = poll(t, m, env)
	assert.Equal(t, []monitor.Stat{
		{Label: "Done", Value: "Whites wash", Tone: monitor.ToneGood},
		{Label: "Done", Value: "Towel wash", Tone: monitor.ToneGood},
		{Label: "Done", Value: "Waitrose order", Tone: monitor.ToneGood},
	}, res.Summary)

	// Tomorrow only the black tee wash is planned; once it's done the day
	// after has nothing.
	tomorrow := at(env, now.AddDate(0, 0, 1))
	assert.Equal(t, []monitor.Stat{{Label: "Today", Value: "Black tee wash", Tone: monitor.ToneAccent}}, poll(t, m, tomorrow).Summary)
	act(t, m, tomorrow, "toggle", "black-tee-wash")
	assert.Equal(t, []monitor.Stat{{Label: "Today", Value: "Nothing to do", Tone: monitor.ToneMuted}}, poll(t, m, at(env, now.AddDate(0, 0, 2))).Summary)

	// A week on, whites and towels are both due, and fit around each
	// other a day apart.
	week := at(env, now.AddDate(0, 0, 7))
	today, later = lines(t, poll(t, m, week))
	assert.Equal(t, []string{"[ ] Whites wash", "[ ] Waitrose order"}, today)
	assert.Contains(t, later, "[ ] Towel wash · tomorrow")
	assert.Contains(t, later, "[ ] Black tee wash · Thu", "done Wednesday, due again next Wednesday, pushed on a day by the towels")
}

func TestLateAndPutOff(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	act(t, m, env, "toggle", "waitrose-order")
	act(t, m, env, "delete", "whites-wash")
	today, later := lines(t, poll(t, m, env))
	assert.Equal(t, []string{"[ ] Black tee wash", "[x] Waitrose order"}, today, "the black tees take the washer the whites left")
	assert.Equal(t, "[ ] Whites wash · tomorrow", later[0])
	assert.Equal(t, "[ ] Towel wash · Thu", later[1])

	// Not ordered for ten days: three days past due, with no flex.
	res := poll(t, m, at(env, now.AddDate(0, 0, 10)))
	today, _ = lines(t, res)
	assert.Contains(t, today, "[ ] Waitrose order · 3 days late")
	assert.Equal(t, monitor.ToneWarn, res.Blocks[0].Items[len(today)-1].Tone)

	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "delete", ID: "waitrose-order"}), monitor.ErrBadAction, "done today")
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "toggle", ID: "nope"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "add", Text: "x"}), monitor.ErrBadAction)
}

func TestReminder(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now.Add(-2 * time.Hour)) // 07:00
	res := poll(t, m, env)
	assert.Empty(t, res.Events)
	assert.Equal(t, time.Hour+time.Second, res.Next, "polls again at 08:00")

	res = poll(t, m, monitortest.Env(now))
	require.Len(t, res.Events, 1)
	assert.Equal(t, "today/2026-10-13", res.Events[0].Key)
	assert.Equal(t, "Whites wash, Waitrose order", res.Events[0].Body)
	assert.Equal(t, 15*time.Hour+time.Second, res.Next, "then at midnight")

	quiet := newMonitor(t, `{"remindAt": "", "chores": [{"name": "Bins", "every": 7}]}`)
	assert.Empty(t, poll(t, quiet, monitortest.Env(now)).Events)
}

// TestSimulated does every chore on the day it's planned for two months,
// and checks the washer never does two a day and nothing slips far.
func TestSimulated(t *testing.T) {
	m := newMonitor(t, `{
		"chores": [
			{"name": "Whites wash", "every": 7, "uses": "washer"},
			{"name": "Black tee wash", "every": 7, "uses": "washer"},
			{"name": "Underwear wash", "every": 7, "uses": "washer"},
			{"name": "Sock wash", "every": 7, "uses": "washer"},
			{"name": "Towel wash", "every": 7, "uses": "washer"},
			{"name": "Bedsheet wash", "every": 14, "flex": 3, "days": ["Sat", "Sun"], "uses": "washer"},
			{"name": "Waitrose order", "every": 7, "flex": 0}
		]
	}`)
	env := monitortest.Env(now)
	var log []string
	for d := range 60 {
		e := at(env, now.AddDate(0, 0, d))
		res := poll(t, m, e)
		washes := 0
		var names []string
		for _, it := range res.Blocks[0].Items {
			require.False(t, it.Done)
			assert.NotContains(t, it.Title, "late", "day %d", d)
			if it.ID != "waitrose-order" {
				washes++
			}
			names = append(names, it.Title)
			act(t, m, e, "toggle", it.ID)
		}
		assert.LessOrEqual(t, washes, 1, "day %d", d)
		log = append(log, now.AddDate(0, 0, d).Format("Mon 2 Jan")+": "+strings.Join(names, ", "))
	}
	t.Log("\n" + strings.Join(log, "\n"))
	// Every chore got done about as often as it should.
	v, err := m.view(env)
	require.NoError(t, err)
	for _, c := range m.chores {
		n := len(v.records[c.id].Done)
		assert.InDelta(t, 60/c.every, n, 1, c.name)
	}
}

func TestConfig(t *testing.T) {
	m := newMonitor(t, `{"chores": [{"name": "Black tee wash!", "every": 3, "days": ["saturday", "Sun"]}]}`)
	assert.Equal(t, "black-tee-wash", m.chores[0].id)
	assert.True(t, m.chores[0].days[time.Saturday] && m.chores[0].days[time.Sunday] && !m.chores[0].any)
	for _, bad := range []string{
		`{}`,
		`{"chores": [{"name": "A", "every": 0}]}`,
		`{"chores": [{"name": "", "every": 2}]}`,
		`{"chores": [{"name": "A", "every": 2}, {"name": "a", "every": 2}]}`,
		`{"chores": [{"name": "A", "every": 2, "flex": 2}]}`,
		`{"chores": [{"name": "A", "every": 2, "days": ["Funday"]}]}`,
		`{"chores": [{"name": "A", "every": 2}], "remindAt": "8am"}`,
		`{"chores": [{"name": "A", "every": 2}], "perDay": {"washer": 0}}`,
		`{"chores": [{"name": "A", "every": 2}], "timezone": "Mars/Base"}`,
	} {
		_, err := New(json.RawMessage(bad))
		assert.Error(t, err, bad)
	}
}

func actData(t *testing.T, m *Monitor, env monitor.Env, typ, id string, data any) error {
	t.Helper()
	b, err := json.Marshal(data)
	require.NoError(t, err)
	return m.Act(context.Background(), env, monitor.Action{Type: typ, ID: id, Data: b})
}

func TestDoneOnADay(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	// The Waitrose order was placed on Saturday, as an email says.
	require.NoError(t, actData(t, m, env, "done", "waitrose-order", Done{Day: "2026-10-10"}))
	require.NoError(t, actData(t, m, env, "done", "waitrose-order", Done{Day: "2026-10-03"}), "an older one goes in order")
	today, later := lines(t, poll(t, m, env))
	assert.Equal(t, []string{"[ ] Whites wash"}, today)
	assert.Contains(t, later, "[ ] Waitrose order · Sat")
	v, err := m.view(env)
	require.NoError(t, err)
	assert.Equal(t, []string{"2026-10-03", "2026-10-10"}, v.records["waitrose-order"].Done)

	assert.ErrorIs(t, actData(t, m, env, "done", "waitrose-order", Done{Day: "2026-10-20"}), monitor.ErrBadAction, "the future")
	assert.ErrorIs(t, actData(t, m, env, "done", "waitrose-order", Done{Day: "Sat"}), monitor.ErrBadAction)
	assert.ErrorIs(t, actData(t, m, env, "done", "nope", Done{Day: "2026-10-10"}), monitor.ErrBadAction)
}

func TestTracked(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	require.NoError(t, actData(t, m, env, "track", "dishwasher-tablets", Tracked{Name: "Dishwasher tablets", Every: 30, Last: "2026-09-10"}))
	require.NoError(t, actData(t, m, env, "track", "coffee-beans", Tracked{Name: "Coffee beans", Every: 21, Last: "2026-10-01"}))
	res := poll(t, m, env)
	require.Len(t, res.Blocks, 3)
	r := res.Blocks[2]
	assert.Equal(t, "Reorders", r.Title)
	assert.Equal(t, "Stop tracking", r.DeleteHint)
	var got []string
	for _, it := range r.Items {
		got = append(got, it.Title)
	}
	assert.Equal(t, []string{"Dishwasher tablets · due", "Coffee beans · Thu 22 Oct"}, got)
	assert.Equal(t, monitor.ToneWarn, r.Items[0].Tone)
	assert.Contains(t, res.Summary, monitor.Stat{Label: "Today", Value: "Reorder dishwasher tablets", Tone: monitor.ToneWarn}, "three days past due")
	require.Len(t, res.Events, 1)
	assert.Equal(t, "Whites wash, Waitrose order, Reorder dishwasher tablets", res.Events[0].Body)
	today, later := lines(t, monitor.Result{Blocks: res.Blocks[:2]})
	assert.NotContains(t, append(today, later...), "Coffee beans", "reorders keep to their own list")

	// Ordered again (an email says, or ticked): next due from then.
	require.NoError(t, actData(t, m, env, "track", "dishwasher-tablets", Tracked{Name: "Dishwasher tablets", Every: 28, Last: "2026-10-12"}))
	res = poll(t, m, env)
	assert.Equal(t, "Dishwasher tablets · Mon 9 Nov", res.Blocks[2].Items[1].Title)

	// Stopping it is for good.
	act(t, m, env, "delete", "coffee-beans")
	require.NoError(t, actData(t, m, env, "track", "coffee-beans", Tracked{Name: "Coffee beans", Every: 21}))
	res = poll(t, m, env)
	require.Len(t, res.Blocks[2].Items, 1)

	assert.ErrorIs(t, actData(t, m, env, "track", "whites-wash", Tracked{Name: "Whites", Every: 7}), monitor.ErrBadAction)
	assert.ErrorIs(t, actData(t, m, env, "track", "x", Tracked{Name: "X", Every: 0}), monitor.ErrBadAction)
}

func TestMove(t *testing.T) {
	m := newMonitor(t, laundry)
	env := monitortest.Env(now)
	move := func(id, dir string) {
		require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "move", ID: id, Text: dir}))
	}

	// Towels from Thursday to tomorrow: the black tees make way.
	move("towel-wash", "up")
	today, later := lines(t, poll(t, m, env))
	assert.Equal(t, []string{"[ ] Whites wash", "[ ] Waitrose order"}, today)
	assert.Equal(t, []string{"[ ] Towel wash · tomorrow", "[ ] Black tee wash · Thu", "[ ] Bedsheet wash · Sat"}, later)

	// Whites off today: down a day, and the towels stay pinned, so the
	// black tees take today.
	move("whites-wash", "down")
	today, later = lines(t, poll(t, m, env))
	assert.Equal(t, []string{"[ ] Black tee wash", "[ ] Waitrose order"}, today)
	assert.Contains(t, later, "[ ] Towel wash · tomorrow")
	assert.Contains(t, later, "[ ] Whites wash · tomorrow", "pinned there, room or not")

	// Today's as early as it goes; done, the pin's forgotten.
	move("black-tee-wash", "up")
	today, _ = lines(t, poll(t, m, env))
	assert.Equal(t, "[ ] Black tee wash", today[0])
	act(t, m, env, "toggle", "towel-wash")
	v, err := m.view(env)
	require.NoError(t, err)
	assert.Empty(t, v.records["towel-wash"].On)

	// A pin for a day that's gone counts for nothing.
	res := poll(t, m, at(env, now.AddDate(0, 0, 3)))
	_, later = lines(t, res)
	assert.NotContains(t, later, "[ ] Whites wash · tomorrow")

	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "move", ID: "whites-wash", Text: "sideways"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "move", ID: "towel-wash", Text: "up"}), monitor.ErrBadAction, "done today")
}
