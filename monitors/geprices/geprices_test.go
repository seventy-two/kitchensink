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
	require.Len(t, res.Blocks[0].Items, 1)
	assert.Equal(t, "Ascension crossbows · 161.6m – 162.5m · alert ≤ 150m", res.Blocks[0].Items[0].Title)
	assert.False(t, res.Blocks[0].Items[0].Done)
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
	assert.True(t, res.Blocks[0].Items[0].Done)
	assert.Equal(t, monitor.ToneGood, res.Summary[0].Tone)

	// Once hit, it stays quiet until it's turned back on.
	*now = now.Add(5 * time.Minute)
	f.set(34681, price{Low: 149_000_000, LowTime: now.Unix()})
	res, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)

	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "toggle", ID: "34681"}))
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
	assert.Contains(t, res.Blocks[0].Items[0].Title, "alert ≥ 2m")

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
	// /mapping, then one /latest; the polls a minute apart reuse it.
	assert.Equal(t, []string{"/mapping", "/latest?id=34681"}, f.requests())

	*now = now.Add(2 * time.Minute)
	_, err := m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Len(t, f.requests(), 3)

	// A new item is fetched at once; the item list is remembered.
	require.NoError(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "abyssal whip"}))
	_, err = m.Poll(ctx, *env)
	require.NoError(t, err)
	assert.Equal(t, []string{"/mapping", "/latest?id=34681", "/latest?id=34681", "/latest?id=34681", "/latest?id=4151"}, f.requests())
}

func TestLookup(t *testing.T) {
	m, _, env, _ := setup(t)
	ctx := context.Background()
	err := m.Act(ctx, *env, monitor.Action{Type: "add", Text: "crossbow"})
	assert.ErrorIs(t, err, monitor.ErrBadAction)
	assert.Contains(t, err.Error(), "Rune crossbow")
	assert.ErrorIs(t, m.Act(ctx, *env, monitor.Action{Type: "add", Text: "twisted bow"}), monitor.ErrBadAction)
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
