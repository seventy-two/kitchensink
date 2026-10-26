package reverb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 21:30 in the UK on 26 October.
var now = time.Date(2026, 10, 26, 21, 30, 0, 0, time.UTC)

type fakeReverb struct {
	*httptest.Server
	mu       sync.Mutex
	listings map[string][]map[string]any // by query
	requests []*http.Request
}

func newFakeReverb(t *testing.T) *fakeReverb {
	f := &fakeReverb{listings: map[string][]map[string]any{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/listings" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r)
		ls := f.listings[r.URL.Query().Get("query")]
		if ls == nil {
			ls = []map[string]any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"listings": ls})
	}))
	t.Cleanup(f.Close)
	return f
}

func item(id int64, title, price, postage string, published time.Time) map[string]any {
	l := map[string]any{
		"id":           id,
		"title":        title,
		"description":  "<p>Plays &amp; sounds <b>great</b>.</p>",
		"condition":    map[string]any{"display_name": "Very Good"},
		"categories":   []any{map[string]any{"full_name": "Electric Guitars / Solid Body"}},
		"price":        map[string]any{"display": price},
		"published_at": published.Format(time.RFC3339),
		"photos":       []any{map[string]any{"_links": map[string]any{"large_crop": map[string]any{"href": "https://rvb-img.reverb.com/" + title + ".jpg"}}}},
		"_links":       map[string]any{"web": map[string]any{"href": "https://reverb.com/item/" + title}},
	}
	if postage != "" {
		l["shipping"] = map[string]any{"user_region_rate": map[string]any{"rate": map[string]any{"display": postage}}}
	}
	return l
}

type fakeGemini struct {
	*httptest.Server
	mu       sync.Mutex
	verdicts map[string]verdict // by title
	asked    [][]string
	inputs   []string
	fail     bool
}

func newFakeGemini(t *testing.T) *fakeGemini {
	g := &fakeGemini{verdicts: map[string]verdict{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input, System_instruction string
		}
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		g.inputs = append(g.inputs, req.Input)
		var list []struct{ ID, Title string }
		json.Unmarshal([]byte(req.Input[strings.Index(req.Input, "["):]), &list)
		var titles []string
		var out []map[string]any
		for _, l := range list {
			titles = append(titles, l.Title)
			if v, ok := g.verdicts[l.Title]; ok {
				out = append(out, map[string]any{"id": l.ID, "match": v.Match, "name": v.Name, "note": v.Note})
			}
		}
		g.asked = append(g.asked, titles)
		text, _ := json.Marshal(out)
		json.NewEncoder(w).Encode(map[string]any{"steps": []any{map[string]any{
			"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}},
		}}})
	}))
	t.Cleanup(g.Close)
	return g
}

func newMonitor(t *testing.T, api, gem string) *Monitor {
	c := map[string]any{
		"searches": []string{"ibanez xiphos", "schecter km-6"},
		"token":    "tok",
		"want":     "An Ibanez Xiphos, or a Schecter KM-6 Mk I or Mk II.",
		"apiURL":   api,
	}
	if gem != "" {
		c["gemini"] = map[string]any{"apiKey": "k", "url": gem}
	}
	raw, _ := json.Marshal(c)
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestPoll(t *testing.T) {
	rv, gem := newFakeReverb(t), newFakeGemini(t)
	xpt := "Ibanez XPT700-RCM 2009 Red Chameleon"
	body := "Walnut Xiphos guitar body fits Ibanez RG necks"
	km := "Schecter KM-6 2017 Natural Pearl"
	km3 := "Schecter KM-6 MK-III Legacy"
	rv.listings["ibanez xiphos"] = []map[string]any{
		item(2, body, "£436.68", "+£20", now.Add(-time.Hour)),
		item(1, xpt, "£875", "£14.99", now.Add(-50*time.Hour)),
	}
	rv.listings["schecter km-6"] = []map[string]any{
		item(4, km3, "£1,480.95", "FREE", now.Add(-30*time.Minute)),
		item(3, km, "£1,099", "FREE", now.Add(-2*time.Hour)),
		item(1, xpt, "£875", "£14.99", now.Add(-50*time.Hour)),
	}
	gem.verdicts[xpt] = verdict{true, "Ibanez XPT700 Xiphos, Red Chameleon", "2009"}
	gem.verdicts[body] = verdict{false, "Xiphos-shaped body", "body only"}
	gem.verdicts[km] = verdict{true, "Schecter KM-6, Natural Pearl", "Mk II, 2017"}
	gem.verdicts[km3] = verdict{false, "Schecter KM-6 Mk III Legacy", "Mk III"}
	m := newMonitor(t, rv.URL, gem.URL)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "listings already up at the first check aren't new")
	q := rv.requests[0]
	assert.Equal(t, "published_at|desc", q.URL.Query().Get("sort"))
	assert.Equal(t, "GB", q.URL.Query().Get("ships_to"))
	assert.Equal(t, "GBP", q.Header.Get("X-Display-Currency"))
	assert.Equal(t, "GB", q.Header.Get("X-Shipping-Region"))
	assert.Equal(t, [][]string{{km3, body, km, xpt}}, gem.asked, "newest first, each once")
	assert.Contains(t, gem.inputs[0], "Plays \\u0026 sounds great", "descriptions as plain text")

	rows := res.Blocks[0].Rows
	require.Len(t, rows, 2, "only the matches")
	assert.Equal(t, "Schecter KM-6, Natural Pearl", rows[0][0].Text)
	assert.Equal(t, "Very Good · Mk II, 2017", rows[0][0].Detail)
	assert.Equal(t, "https://reverb.com/item/"+km, rows[0][0].URL)
	assert.Equal(t, "£1,099", rows[0][1].Text)
	assert.Equal(t, "free postage", rows[0][1].Detail)
	assert.Equal(t, "19:30", rows[0][2].Text)
	assert.Equal(t, "Ibanez XPT700 Xiphos, Red Chameleon", rows[1][0].Text)
	assert.Equal(t, "+ £14.99 postage", rows[1][1].Detail)
	assert.Equal(t, "Sat 20:30", rows[1][2].Text, "still summer time then")
	require.NotNil(t, rows[1][3].Action)
	assert.Equal(t, monitor.CellAction{Type: "reject", ID: "1", Label: "Not what I'm after"}, *rows[1][3].Action)

	// New listings: only the match is reported, and nothing's asked twice.
	xpt2 := "Ibanez XPT707 Black"
	egen := "Ibanez EGEN8 Herman Li"
	gem.verdicts[xpt2] = verdict{true, "Ibanez XPT707 Xiphos, Black", "7-string"}
	gem.verdicts[egen] = verdict{false, "Ibanez EGEN8", "not wanted"}
	rv.listings["ibanez xiphos"] = append([]map[string]any{
		item(5, xpt2, "£700", "", now.Add(-time.Minute)),
		item(6, egen, "£800", "", now.Add(-2*time.Minute)),
	}, rv.listings["ibanez xiphos"]...)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/5"}, monitortest.Keys(res.Events))
	e := res.Events[0]
	assert.Equal(t, "Ibanez XPT707 Xiphos, Black", e.Title)
	assert.Equal(t, "£700 · 7-string", e.Body)
	assert.Equal(t, "https://rvb-img.reverb.com/"+xpt2+".jpg", e.Image)
	assert.Equal(t, [][]string{{xpt2, egen}}, gem.asked[1:])
	assert.Len(t, res.Blocks[0].Rows, 3)
}

func TestGeminiDown(t *testing.T) {
	rv, gem := newFakeReverb(t), newFakeGemini(t)
	m := newMonitor(t, rv.URL, gem.URL)
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	title := "Ibanez XPTB620 Iron Label"
	rv.listings["ibanez xiphos"] = []map[string]any{item(9, title, "£1,169", "", now)}
	gem.fail = true
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)
	assert.Empty(t, res.Blocks[0].Rows, "held back until it's been checked")

	gem.fail = false
	gem.verdicts[title] = verdict{true, "Ibanez XPTB620 Iron Label Xiphos, Black Flat", ""}
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/9"}, monitortest.Keys(res.Events), "still new once checked")
	assert.Equal(t, "£1,169", res.Events[0].Body)
}

func TestBatches(t *testing.T) {
	rv, gem := newFakeReverb(t), newFakeGemini(t)
	var ls []map[string]any
	for i := range geminiBatch + 5 {
		title := "XPT " + string(rune('A'+i))
		ls = append(ls, item(int64(i+1), title, "£1", "", now.Add(-time.Duration(i)*time.Minute)))
		gem.verdicts[title] = verdict{true, title, ""}
	}
	rv.listings["ibanez xiphos"] = ls
	m := newMonitor(t, rv.URL, gem.URL)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	require.Len(t, gem.asked, 2)
	assert.Len(t, gem.asked[0], geminiBatch)
	assert.Len(t, gem.asked[1], 5)
	assert.Len(t, res.Blocks[0].Rows, geminiBatch+5)
}

func TestWithoutGemini(t *testing.T) {
	rv := newFakeReverb(t)
	rv.listings["ibanez xiphos"] = []map[string]any{item(1, "Ibanez Xiphos", "£900", "", now)}
	m := newMonitor(t, rv.URL, "")
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	rows := res.Blocks[0].Rows
	require.Len(t, rows, 1, "everything's shown")
	assert.Equal(t, "Ibanez Xiphos", rows[0][0].Text)
	assert.Equal(t, "Very Good", rows[0][0].Detail)
	assert.Nil(t, rows[0][3].Action)
}

func TestReject(t *testing.T) {
	rv, gem := newFakeReverb(t), newFakeGemini(t)
	title := "Schecter KM-6 MK-II Hybrid"
	rv.listings["schecter km-6"] = []map[string]any{item(7, title, "£700", "", now)}
	gem.verdicts[title] = verdict{true, "Schecter KM-6 Mk II Hybrid", "Mk II"}
	m := newMonitor(t, rv.URL, gem.URL)
	env := monitortest.Env(now)
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks[0].Rows, 1)

	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "reject", ID: "7"}))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Blocks[0].Rows)

	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "reject", ID: "99"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "nope"}), monitor.ErrBadAction)
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{"token":"t"}`))
	assert.ErrorContains(t, err, "searches")
	_, err = New(json.RawMessage(`{"searches":["x"]}`))
	assert.ErrorContains(t, err, "token")
	_, err = New(json.RawMessage(`{"searches":["x"],"token":"t","gemini":{"apiKey":"k"}}`))
	assert.ErrorContains(t, err, "want")
	m, err := New(json.RawMessage(`{"searches":["x"],"token":"t","shipsTo":"any"}`))
	require.NoError(t, err)
	assert.Equal(t, "", m.cfg.ShipsTo)
}
