package geprices

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAPI serves /mapping and /latest from prices, counting requests.
type fakeAPI struct {
	mu     sync.Mutex
	prices map[int]price
	paths  []string
	agents []string
}

func (f *fakeAPI) set(id int, p price) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prices[id] = p
}

func (f *fakeAPI) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func newFake(t *testing.T) (*fakeAPI, *httptest.Server) {
	f := &fakeAPI{prices: map[int]price{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.RequestURI())
		f.agents = append(f.agents, r.UserAgent())
		switch r.URL.Path {
		case "/mapping":
			json.NewEncoder(w).Encode([]mapped{
				{ID: 34681, Name: "Ascension crossbows"},
				{ID: 11785, Name: "Armadyl crossbow"},
				{ID: 9185, Name: "Rune crossbow"},
				{ID: 4151, Name: "Abyssal whip"},
			})
		case "/timeseries":
			now := int64(1795259500)
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
				{"timestamp": now - 20*3600, "avgHighPrice": 170_000_000, "avgLowPrice": 166_000_000},
				{"timestamp": now - 3600, "avgHighPrice": 163_000_000, "avgLowPrice": nil},
			}})
		case "/latest":
			data := map[string]price{}
			for id, p := range f.prices {
				if q := r.URL.Query().Get("id"); q == "" || q == fmt.Sprint(id) {
					data[fmt.Sprint(id)] = p
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func setup(t *testing.T) (*Monitor, *fakeAPI, *monitor.Env, *time.Time) {
	f, srv := newFake(t)
	m, err := New(json.RawMessage(fmt.Sprintf(`{"url": %q}`, srv.URL)))
	require.NoError(t, err)
	now := time.Unix(1795259500, 0)
	env := monitortest.Env(now)
	env.Now = func() time.Time { return now }
	return m, f, &env, &now
}

func TestParseAdd(t *testing.T) {
	for _, c := range []struct {
		in     string
		query  string
		target int64
		dir    byte
	}{
		{"ascension crossbows 150m", "ascension crossbows", 150_000_000, 0},
		{"ascension crossbows <150m", "ascension crossbows", 150_000_000, '<'},
		{"Twisted bow >1.25b", "Twisted bow", 1_250_000_000, '>'},
		{"abyssal whip 1,500,000", "abyssal whip", 1_500_000, 0},
		{"abyssal whip 900k", "abyssal whip", 900_000, 0},
		{"abyssal whip", "abyssal whip", 0, 0},
		{"Dragon dart 4", "Dragon dart 4", 0, 0},
		{"4151 2m", "4151", 2_000_000, 0},
	} {
		q, v, d, err := parseAdd(c.in)
		require.NoError(t, err, c.in)
		assert.Equal(t, c.query, q, c.in)
		assert.Equal(t, c.target, v, c.in)
		assert.Equal(t, c.dir, d, c.in)
	}
	for _, bad := range []string{"", "   ", "whip <lots"} {
		_, _, _, err := parseAdd(bad)
		assert.Error(t, err, bad)
	}
}

func TestCoins(t *testing.T) {
	assert.Equal(t, "162.5m", coins(162_500_000))
	assert.Equal(t, "161.6m", coins(161_567_890))
	assert.Equal(t, "150m", coins(150_000_000))
	assert.Equal(t, "1.25b", coins(1_250_000_000))
	assert.Equal(t, "2.5m", coins(2_500_000))
	assert.Equal(t, "900k", coins(900_000))
	assert.Equal(t, "750", coins(750))
}

func TestAlertFiresOnceOnAFallToTarget(t *testing.T) {
	m, f, env, now := setup(t)
	ctx := context.Background()
	f.set(34681, price{High: 162_500_000, HighTime: now.Unix() - 50, Low: 161_567_890, LowTime: now.Unix() - 300})

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "ascension crossbows 150m"}))
	res, err := m.Poll(ctx, *env)
	require.NoError(t, err)
	chip := res.Blocks[0].Stats[0]
	assert.Equal(t, "Ascension crossbows", chip.Label)
	assert.Equal(t, "162.5m", chip.Value)
	require.NotNil(t, chip.Spark)
	assert.Equal(t, monitor.ToneBad, chip.Spark.Tone)
	assert.Equal(t, "-3.3%", chip.Detail)
	assert.Equal(t, chip, res.Summary[0])
	row := res.Settings[0].Rows[0]
	assert.Equal(t, "<150m", row[2].Input.Value)
	assert.Equal(t, "watching for a fall", row[3].Text)
	assert.Equal(t, "add", res.Settings[0].Rows[1][0].Input.Action)
	assert.Empty(t, res.Events)

	// A sale at 150m before it was armed doesn't count.
	*now = now.Add(5 * time.Minute)
	f.set(34681, price{High: 151_000_000, HighTime: now.Unix(), Low: 150_000_000, LowTime: now.Unix() - 3600})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)

	*now = now.Add(5 * time.Minute)
	f.set(34681, price{High: 151_000_000, HighTime: now.Unix(), Low: 149_900_000, LowTime: now.Unix() - 10})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "Ascension crossbows has fallen to 149.9m", res.Events[0].Title)
	assert.Equal(t, monitor.PriorityHigh, res.Events[0].Priority)
	assert.Equal(t, "https://prices.runescape.wiki/osrs/item/34681", res.Events[0].URL)
	assert.Equal(t, monitor.ToneGood, res.Summary[0].Tone)
	assert.Contains(t, res.Settings[0].Rows[0][3].Text, "hit 149.9m")

	// Once hit, it stays quiet until its target is set again.
	*now = now.Add(5 * time.Minute)
	f.set(34681, price{Low: 149_000_000, LowTime: now.Unix()})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: "<150m"}))
	*now = now.Add(5 * time.Minute)
	f.set(34681, price{Low: 148_000_000, LowTime: now.Unix()})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Contains(t, res.Events[0].Key, "hit/34681/")

	for _, a := range f.agents {
		assert.Contains(t, a, "kitchensink")
	}
}

func TestRiseInferredFromPrice(t *testing.T) {
	m, f, env, now := setup(t)
	ctx := context.Background()
	f.set(4151, price{High: 1_500_000, HighTime: now.Unix(), Low: 1_450_000, LowTime: now.Unix()})
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "abyssal whip 2m"}))
	res, err := m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Equal(t, ">2m", res.Settings[0].Rows[0][2].Input.Value)
	assert.Equal(t, "watching for a rise", res.Settings[0].Rows[0][3].Text)

	*now = now.Add(5 * time.Minute)
	f.set(4151, price{High: 2_000_000, HighTime: now.Unix(), Low: 1_900_000, LowTime: now.Unix()})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "Abyssal whip has risen to 2m", res.Events[0].Title)
}

func TestFetchesAtMostOncePerInterval(t *testing.T) {
	m, f, env, now := setup(t)
	ctx := context.Background()
	f.set(34681, price{High: 162_500_000, HighTime: now.Unix()})
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "ascension crossbows"}))
	for range 3 {
		_, err := m.Poll(ctx, *env)
		require.NoError(t, err)
		*now = now.Add(time.Minute)
	}
	// /mapping, then one /latest and the chart; the polls a minute apart
	// reuse them.
	assert.Equal(t, []string{"/mapping", "/latest?id=34681", "/timeseries?timestep=5m&id=34681"}, f.requests())

	*now = now.Add(2 * time.Minute)
	_, err := m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Len(t, f.requests(), 4)

	// A new item is fetched at once; the item list is remembered.
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "abyssal whip"}))
	_, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Equal(t, []string{"/mapping", "/latest?id=34681", "/timeseries?timestep=5m&id=34681",
		"/latest?id=34681", "/latest?id=34681", "/latest?id=4151", "/timeseries?timestep=5m&id=4151"}, f.requests())
}

func TestLookup(t *testing.T) {
	m, _, env, _ := setup(t)
	ctx := context.Background()
	// Bad adds are said in the settings, as clients don't hear back.
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "crossbow"}))
	res, err := m.Poll(ctx, *env)
	require.NoError(t, err)
	require.Len(t, res.Settings, 2)
	assert.Contains(t, res.Settings[0].Rows[0][0].Text, "Rune crossbow")
	assert.Equal(t, monitor.ToneBad, res.Settings[0].Rows[0][0].Tone)
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "twisted bow"}))
	assert.Contains(t, m.notice, "no GE item called")
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "ASCENSION"}))
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "11785"}))

	items, err := load(*env)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Ascension crossbows", items[0].Name)
	assert.Equal(t, "Armadyl crossbow", items[1].Name)

	// Adding again changes the target rather than adding a second row.
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "ascension crossbows <140m"}))
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "delete", ID: "11785"}))
	items, err = load(*env)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, int64(140_000_000), items[0].Target)
	assert.ErrorIs(t, m.Act(ctx, *env, monitor.Action{Type: "delete", ID: "11785"}), monitor.ErrBadAction)
}

func TestTargetAction(t *testing.T) {
	m, f, env, now := setup(t)
	ctx := context.Background()
	f.set(34681, price{High: 162_500_000, HighTime: now.Unix(), Low: 161_000_000, LowTime: now.Unix()})
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "ascension crossbows"}))
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: "150m"}))
	items, err := load(*env)
	require.NoError(t, err)
	assert.Equal(t, int64(150_000_000), items[0].Target)
	assert.False(t, items[0].Above)

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: " > 200m "}))
	items, _ = load(*env)
	assert.True(t, items[0].Above)

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: ""}))
	items, _ = load(*env)
	assert.Zero(t, items[0].Target)

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: "lots"}))
	assert.Contains(t, m.notice, "isn't a price")
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "target", ID: "34681", Text: "1m"}))
	assert.Empty(t, m.notice)
	assert.ErrorIs(t, m.Act(ctx, *env, monitor.Action{Type: "delete", ID: "1"}), monitor.ErrBadAction)
}

func TestChart(t *testing.T) {
	now := time.Unix(1795259500, 0)
	assert.Nil(t, chart(nil, now, 0))
	assert.Equal(t, []float64{5, 5}, chart(nil, now, 5))
	pts := chart([]trade{
		{Time: now.Unix() - 23*3600, High: 10, Low: 8},
		{Time: now.Unix() - 3600, Low: 6},
		{Time: now.Unix() - 25*3600, High: 99}, // before the day
	}, now, 7)
	require.Len(t, pts, slots+1)
	assert.Equal(t, 9.0, pts[0])  // carried back from the first trade
	assert.Equal(t, 9.0, pts[50]) // carried over
	assert.Equal(t, 6.0, pts[slots-1])
	assert.Equal(t, 7.0, pts[slots])
}
