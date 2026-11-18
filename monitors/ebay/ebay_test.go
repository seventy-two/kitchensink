package ebay

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

// 21:30 in the UK on 14 October.
var now = time.Date(2026, 10, 14, 20, 30, 0, 0, time.UTC)

type fakeEbay struct {
	*httptest.Server
	mu      sync.Mutex
	items   []map[string]any
	byQuery map[string][]map[string]any // answers for a query, else items
	tokens  int
	queries []string
}

func newFakeEbay(t *testing.T) *fakeEbay {
	f := &fakeEbay{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /identity/v1/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		r.ParseForm()
		if user != "id" || pass != "secret" || r.Form.Get("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 7200})
	})
	mux.HandleFunc("GET /buy/browse/v1/item_summary/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-EBAY-C-MARKETPLACE-ID") != "EBAY_GB" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.queries = append(f.queries, r.URL.RawQuery)
		items := f.items
		if q, ok := f.byQuery[r.URL.Query().Get("q")]; ok {
			items = q
		}
		json.NewEncoder(w).Encode(map[string]any{"itemSummaries": items})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func listing(id, title, price string, created time.Time, auction bool, bids int) map[string]any {
	it := map[string]any{
		"itemId":           "v1|" + id + "|0",
		"title":            title,
		"price":            map[string]any{"value": price, "currency": "GBP"},
		"buyingOptions":    []string{"FIXED_PRICE", "BEST_OFFER"},
		"itemWebUrl":       "https://www.ebay.co.uk/itm/" + id + "?_skw=big&hash=x",
		"image":            map[string]any{"imageUrl": "https://i.ebayimg.com/" + id + ".jpg"},
		"itemCreationDate": created.Format(time.RFC3339),
	}
	if auction {
		it["buyingOptions"] = []string{"AUCTION"}
		it["currentBidPrice"] = map[string]any{"value": price, "currency": "GBP"}
		it["bidCount"] = bids
	}
	return it
}

type fakeGemini struct {
	*httptest.Server
	mu     sync.Mutex
	names  map[string]string // by title
	rarity map[string]string // by title
	asked  [][]string
	fail   bool
	// deals answers price questions, by title; priced records what was
	// asked (title: asking) and searched whether search was on.
	deals    map[string]deal
	priced   []map[string]float64
	searched bool
	// systems are the instructions naming requests came with.
	systems []string
}

func newFakeGemini(t *testing.T) *fakeGemini {
	g := &fakeGemini{names: map[string]string{}, rarity: map[string]string{}, deals: map[string]deal{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input  string
			Tools  []any
			System string `json:"system_instruction"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		reply := func(v any) {
			text, _ := json.Marshal(v)
			json.NewEncoder(w).Encode(map[string]any{"steps": []any{map[string]any{
				"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}},
			}}})
		}
		if strings.HasPrefix(req.Input, "Judge each listing's asking price") {
			var list []struct {
				ID, Title string
				Asking    float64
			}
			json.Unmarshal([]byte(req.Input[strings.Index(req.Input, "["):]), &list)
			asked := map[string]float64{}
			var out []deal
			for _, l := range list {
				asked[l.Title] = l.Asking
				if d, ok := g.deals[l.Title]; ok {
					d.ID = l.ID
					out = append(out, d)
				}
			}
			g.priced = append(g.priced, asked)
			g.searched = len(req.Tools) > 0
			reply(out)
			return
		}
		var list []struct{ ID, Title string }
		json.Unmarshal([]byte(req.Input[strings.Index(req.Input, "["):]), &list)
		g.systems = append(g.systems, req.System)
		var titles []string
		var out []map[string]string
		for _, l := range list {
			titles = append(titles, l.Title)
			if n, ok := g.names[l.Title]; ok {
				out = append(out, map[string]string{"id": l.ID, "name": n, "rarity": g.rarity[l.Title]})
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
	return newMonitorWith(t, api, gem, nil)
}

func newMonitorWith(t *testing.T, api, gem string, extra map[string]any) *Monitor {
	c := map[string]any{
		"searchURL":    "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=0&_from=R40&_sop=10",
		"clientId":     "id",
		"clientSecret": "secret",
		"name":         "a boxed PC game",
		"apiURL":       api,
		"gemini":       map[string]any{"apiKey": "k", "url": gem},
	}
	for k, v := range extra {
		c[k] = v
	}
	raw, _ := json.Marshal(c)
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestPoll(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	ultima := "ULTIMA VII BLACK GATE BIG BOX PC GAME ORIGIN RARE VINTAGE 1992 VGC"
	sim := "Sim City 2000 big box pc cd rom maxis complete"
	eb.items = []map[string]any{
		listing("2", sim, "24.99", now.Add(-2*time.Hour), false, 0),
		listing("1", ultima, "45.00", now.Add(-50*time.Hour), true, 3),
	}
	gem.names[ultima] = "Ultima VII: The Black Gate"
	gem.names[sim] = "SimCity 2000"
	m := newMonitor(t, eb.URL, gem.URL)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "listings already up at the first check aren't new")
	assert.Contains(t, eb.queries[0], "q=big+box+pc")
	assert.Contains(t, eb.queries[0], "sort=newlyListed")
	assert.NotContains(t, eb.queries[0], "category_ids", "_sacat=0 is all categories")

	rows := res.Blocks[0].Rows
	require.Len(t, rows, 2)
	assert.Equal(t, "SimCity 2000", rows[0][0].Text)
	assert.Equal(t, "https://www.ebay.co.uk/itm/2", rows[0][0].URL)
	assert.Equal(t, "£24.99", rows[0][2].Text)
	assert.Equal(t, "19:30", rows[0][5].Text)
	assert.Equal(t, "Ultima VII: The Black Gate", rows[1][0].Text)
	assert.Equal(t, "£45 · 3 bids", rows[1][2].Text)
	assert.Equal(t, "Mon 19:30", rows[1][5].Text)
	for _, r := range rows {
		for _, c := range r {
			assert.Empty(t, c.Detail, "one line a row")
		}
	}
	require.NotNil(t, res.SummaryBlock)

	// A new listing: reported, named, and nothing asked about twice.
	half := "half life pc big box sierra 1998 + manual RARE"
	gem.names[half] = "Half-Life"
	eb.items = append([]map[string]any{listing("3", half, "60", now.Add(-time.Minute), false, 0)}, eb.items...)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/v1|3|0"}, monitortest.Keys(res.Events))
	e := res.Events[0]
	assert.False(t, e.Quiet)
	assert.Equal(t, "Half-Life", e.Title)
	assert.Equal(t, "£60", e.Body)
	assert.Equal(t, "https://i.ebayimg.com/3.jpg", e.Image)
	assert.Equal(t, [][]string{{sim, ultima}, {half}}, gem.asked)
	assert.Equal(t, 1, eb.tokens, "the token is reused")
}

func TestGeminiDown(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitor(t, eb.URL, gem.URL)
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	title := "DUNE 2 big box westwood"
	eb.items = []map[string]any{listing("9", title, "30", now, true, 0)}
	gem.fail = true
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "held back until it has a name")
	assert.Equal(t, title, res.Blocks[0].Rows[0][0].Text, "shown by its title meanwhile")
	assert.Equal(t, "£30 · auction", res.Blocks[0].Rows[0][2].Text)

	gem.fail = false
	gem.names[title] = "Dune II"
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/v1|9|0"}, monitortest.Keys(res.Events))
	assert.Equal(t, "Dune II", res.Events[0].Title)
	assert.Equal(t, "£30 · auction", res.Events[0].Body)
}

func TestNeedsKeys(t *testing.T) {
	_, err := New(json.RawMessage(`{"query":"big box pc"}`))
	assert.Error(t, err)
	_, err = New(json.RawMessage(`{"clientId":"a","clientSecret":"b"}`))
	assert.Error(t, err)
}

func TestRarity(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon"})
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	quake := "QUAKE id software big box RARE!! L@@K"
	sin := "SiN pc big box activision 1998"
	ewj := "Earthworm Jim 1 & 2 big box sealed"
	for title, j := range map[string][2]string{
		quake: {"Quake", "common"}, sin: {"SiN", "uncommon"}, ewj: {"Earthworm Jim 1 + 2", "rare"},
	} {
		gem.names[title], gem.rarity[title] = j[0], j[1]
	}
	eb.items = []map[string]any{
		listing("1", quake, "20", now, false, 0),
		listing("2", sin, "35", now, false, 0),
		listing("3", ewj, "150", now, false, 0),
	}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/v1|2|0", "listing/v1|3|0"}, monitortest.Keys(res.Events), "common, so only in the panel")
	assert.False(t, res.Events[0].Quiet)
	assert.Equal(t, "£35 · uncommon", res.Events[0].Body)
	assert.Equal(t, "£150 · rare", res.Events[1].Body)

	rows := res.Blocks[0].Rows
	assert.Equal(t, "", rows[0][1].Text)
	assert.Equal(t, "uncommon", rows[1][1].Text)
	assert.Equal(t, monitor.ToneNone, rows[1][0].Tone)
	assert.Equal(t, "rare", rows[2][1].Text)
	assert.Equal(t, monitor.ToneAccent, rows[2][0].Tone)
	assert.Equal(t, monitor.ToneAccent, rows[2][1].Tone)
}

func TestRarityOfListingsNamedBefore(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "rare"})
	env := monitortest.Env(now)
	title := "DOOM 2 big box gt interactive"
	eb.items = []map[string]any{listing("5", title, "40", now, false, 0)}
	require.NoError(t, env.State.Set(baselineKey, true))
	require.NoError(t, env.State.Set(seenKey, map[string]time.Time{"v1|5|0": now}))
	require.NoError(t, env.State.Set(namesKey, map[string]string{"v1|5|0": "Doom II"}))
	gem.names[title], gem.rarity[title] = "Doom II: Hell on Earth", "common"

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "already reported")
	assert.Equal(t, [][]string{{title}}, gem.asked, "asked how rare it is")
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, gem.asked, 1, "and only once")
}

func TestNotifyMustBeARarity(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"query": "x", "clientId": "id", "clientSecret": "s", "notify": "legendary"})
	_, err := New(raw)
	assert.ErrorContains(t, err, "common, uncommon, rare")
}

func TestIgnore(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon"})
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	wipeout := "WIPEOUT PC BIG BOX PSYGNOSIS"
	quake := "quake big box"
	gem.names[wipeout], gem.rarity[wipeout] = "WipEout", "rare"
	gem.names[quake], gem.rarity[quake] = "Quake", "common"
	eb.items = []map[string]any{listing("1", wipeout, "90", now, false, 0), listing("2", quake, "10", now, false, 0)}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	rows := res.Blocks[0].Rows
	require.NotNil(t, rows[0][7].Action, "a rare listing can be ignored")
	assert.Equal(t, monitor.CellAction{Type: "ignore", ID: "v1|1|0", Label: "Ignore WipEout from now on"}, *rows[0][7].Action)
	assert.Nil(t, rows[1][7].Action, "a common one has nothing to ignore")

	require.Equal(t, []string{"listing/v1|1|0"}, monitortest.Keys(res.Events), "notified")

	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "ignore", ID: "v1|1|0"}))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	// Its notification ends, so clients drop it; only once.
	require.Len(t, res.Events, 1)
	assert.Equal(t, monitor.Event{Key: "listing/v1|1|0", Update: true, Title: "WipEout", Body: "Ignored", Until: now}, res.Events[0])
	res2, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res2.Events)
	rows = res.Blocks[0].Rows
	assert.Equal(t, "", rows[0][0].Detail, "now counts as common")
	assert.Equal(t, monitor.ToneNone, rows[0][0].Tone)
	assert.Nil(t, rows[0][7].Action)

	// Another copy, named a little differently, is ignored too.
	again := "Wipeout pc cd rom 1995 RARE"
	gem.names[again], gem.rarity[again] = "Wipeout", "rare"
	eb.items = append([]map[string]any{listing("3", again, "80", now, false, 0)}, eb.items...)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "ignored, so only in the panel")
	assert.Equal(t, "Wipeout", res.Blocks[0].Rows[0][0].Text)
	assert.Equal(t, "", res.Blocks[0].Rows[0][0].Detail)

	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "ignore", ID: "v1|99|0"}), monitor.ErrBadAction)
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "nope"}), monitor.ErrBadAction)
}

func TestShortViewOnlyRecentUncommonAndRare(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	common, uncommon, rare, old := "doom 2 big box", "lba big box", "wipeout big box", "croc big box"
	gem.names[common], gem.rarity[common] = "Doom II", "common"
	gem.names[uncommon], gem.rarity[uncommon] = "Little Big Adventure", "uncommon"
	gem.names[rare], gem.rarity[rare] = "WipEout", "rare"
	gem.names[old], gem.rarity[old] = "Croc", "rare"
	eb.items = []map[string]any{
		listing("1", common, "10", now.Add(-time.Hour), false, 0),
		listing("2", uncommon, "20", now.Add(-2*time.Hour), false, 0),
		listing("3", rare, "30", now.Add(-3*time.Hour), false, 0),
		listing("4", old, "40", now.Add(-25*time.Hour), false, 0),
	}
	m := newMonitor(t, eb.URL, gem.URL)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	require.Len(t, res.Blocks[0].Rows, 4, "the panel still lists everything")
	require.NotNil(t, res.SummaryBlock)
	var names []string
	for _, r := range res.SummaryBlock.Rows {
		names = append(names, r[0].Text)
	}
	assert.Equal(t, []string{"Little Big Adventure", "WipEout"}, names, "no common ones, nor any from over a day ago")
	assert.Len(t, res.Summary, 2)
	assert.False(t, res.Background)

	// A day on, nothing: off the dashboard.
	res, err = m.Poll(context.Background(), monitortest.Env(now.Add(22*time.Hour)))
	require.NoError(t, err)
	assert.Empty(t, res.SummaryBlock.Rows)
	assert.Empty(t, res.Summary)
	assert.True(t, res.Background)
}

func withPostage(it map[string]any, cost string) map[string]any {
	it["shippingOptions"] = []any{
		map[string]any{"shippingCostType": "FIXED", "shippingCost": map[string]any{"value": cost, "currency": "GBP"}},
		map[string]any{"shippingCostType": "FIXED", "shippingCost": map[string]any{"value": "99.00", "currency": "GBP"}},
	}
	return it
}

func TestGrailsAndPostage(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon",
		"grails": []string{"Wipeout", "Metal Gear Solid*", "Black & White", "Croc 2"}})
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	for title, j := range map[string][2]string{
		"wipeout pc big box psygnosis":      {"WipEout", "common"},
		"wipeout 2097 pc big box":           {"Wipeout 2097", "rare"},
		"MGS2 substance pc":                 {"Metal Gear Solid 2: Substance", "common"},
		"black and white lionhead":          {"Black & White", "common"},
		"croc 1 and 2 lot":                  {"Croc: Legend of the Gobbos + Croc 2", "uncommon"},
		"metal gear (msx) not solid":        {"Metal Gear", "common"},
		"black & white creature isle addon": {"Black & White: Creature Isle", "common"},
	} {
		gem.names[title], gem.rarity[title] = j[0], j[1]
	}
	eb.items = []map[string]any{
		withPostage(listing("1", "wipeout pc big box psygnosis", "20", now, false, 0), "10.01"),
		listing("2", "wipeout 2097 pc big box", "40", now, false, 0),
		withPostage(listing("3", "MGS2 substance pc", "20", now, false, 0), "10.00"),
		listing("4", "black and white lionhead", "15", now, false, 0),
		listing("5", "croc 1 and 2 lot", "30", now, false, 0),
		listing("6", "metal gear (msx) not solid", "30", now, false, 0),
		listing("7", "black & white creature isle addon", "5", now, false, 0),
		withPostage(listing("8", "wipeout pc big box psygnosis", "1", now, true, 0), "5.00"),
	}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	rows := res.Blocks[0].Rows
	tier := func(i int) string { return rows[i][1].Text }
	assert.Equal(t, grail, tier(0), "the 1995 Wipeout")
	assert.Equal(t, monitor.ToneMuted, rows[0][0].Tone, "a grail, greyed out by its postage")
	assert.Equal(t, "rare", tier(1), "its sequel isn't on the list")
	assert.Equal(t, grail, tier(2), "any Metal Gear Solid")
	assert.Equal(t, grail, tier(3), "& and and match")
	assert.Equal(t, grail, tier(4), "a lot with a grail in it")
	assert.Equal(t, "", tier(5), "a prefix only matches whole words of the name")
	assert.Equal(t, "", tier(6), "an expansion isn't the game")

	// Postage: over half a Buy It Now price is marked; half exactly isn't.
	assert.Equal(t, monitor.ToneMuted, rows[0][2].Tone)
	assert.Equal(t, "+£10.01 (high)", rows[0][3].Text, "the cheapest option")
	assert.Equal(t, monitor.ToneNone, rows[2][2].Tone)
	assert.Equal(t, monitor.ToneGood, rows[2][0].Tone, "a grail at a fair postage stands out")
	assert.Equal(t, "+£10", rows[2][3].Text)
	assert.Equal(t, "", rows[1][3].Text, "none given")
	assert.Equal(t, monitor.ToneNone, rows[7][2].Tone, "an auction's final price isn't known")
	assert.Equal(t, "£1 · auction", rows[7][2].Text)
	assert.Equal(t, "+£5", rows[7][3].Text)

	byKey := map[string]monitor.Event{}
	for _, e := range res.Events {
		byKey[e.Key] = e
	}
	assert.Equal(t, monitor.PriorityHigh, byKey["listing/v1|1|0"].Priority, "grails notify loudly")
	assert.Equal(t, "£20 +£10.01 postage (high) · grail", byKey["listing/v1|1|0"].Body)
	assert.Equal(t, monitor.PriorityNormal, byKey["listing/v1|2|0"].Priority)
	assert.NotContains(t, byKey, "listing/v1|6|0", "common, so no event")
}

func TestDeals(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon", "deals": true,
		"prices": map[string]float64{"Croc": 30}})
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	for title, j := range map[string][2]string{
		"lba big box": {"Little Big Adventure", "uncommon"},
		"quake":       {"Quake", "common"},
		"lba auction": {"Little Big Adventure", "uncommon"},
		"croc":        {"Croc", "common"},
		"lba usd":     {"Little Big Adventure", "uncommon"},
	} {
		gem.names[title], gem.rarity[title] = j[0], j[1]
	}
	gem.deals["lba big box"] = deal{Verdict: "good", Low: 25, High: 40, Why: "sells for £25-40 complete"}
	usd := listing("5", "lba usd", "10", now, false, 0)
	usd["price"] = map[string]any{"value": "10", "currency": "USD"}
	eb.items = []map[string]any{
		withPostage(listing("1", "lba big box", "14", now, false, 0), "5.00"),
		listing("2", "quake", "10", now, false, 0),
		listing("3", "lba auction", "5", now, true, 1),
		withPostage(listing("4", "croc", "40", now, false, 0), "0.00"),
		usd,
	}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []map[string]float64{{"lba big box": 19}}, gem.priced,
		"only uncommon-or-better Buy It Now in pounds, with postage; Croc has your price")
	assert.True(t, gem.searched, "looked up")

	rows := res.Blocks[0].Rows
	assert.Equal(t, monitor.Cell{Text: "good deal £25–40", Tone: monitor.ToneGood}, rows[0][4])
	assert.Equal(t, monitor.Cell{}, rows[1][4], "common: not judged")
	assert.Equal(t, monitor.Cell{}, rows[2][4], "an auction: not judged")
	assert.Equal(t, monitor.Cell{Text: "overpriced, yours £30", Tone: monitor.ToneMuted}, rows[3][4], "£40 against your £30")
	assert.Equal(t, monitor.ToneMuted, rows[3][0].Tone, "the whole row greys out")
	assert.Equal(t, monitor.ToneMuted, rows[3][2].Tone)
	assert.Equal(t, monitor.ToneNone, rows[0][0].Tone, "a good deal doesn't")
	assert.Equal(t, monitor.Cell{}, rows[4][4], "not in pounds")
	assert.Equal(t, "£14 +£5 postage · uncommon · good deal", res.Events[0].Body)

	// Judged once a price; a new price is judged again.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, gem.priced, 1)
	eb.items[0] = withPostage(listing("1", "lba big box", "30", now, false, 0), "5.00")
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"lba big box": 35}, gem.priced[1])

	// Out of the model's hands, your price still settles it.
	gem.fail = true
	eb.items[3] = withPostage(listing("4", "croc", "20", now, false, 0), "0.00")
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, "good deal, yours £30", res.Blocks[0].Rows[3][4].Text)
}

func TestHide(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon", "grails": []string{"Need for Speed: Underground 2"}})
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	gem.names["nfsu2 dud"], gem.rarity["nfsu2 dud"] = "Need for Speed: Underground 2", "common"
	gem.names["nfsu2 good"], gem.rarity["nfsu2 good"] = "Need for Speed: Underground 2", "common"
	eb.items = []map[string]any{listing("1", "nfsu2 dud", "150", now, false, 0)}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"listing/v1|1|0"}, monitortest.Keys(res.Events))
	hide := res.Blocks[0].Rows[0][6].Action
	require.NotNil(t, hide)
	assert.Equal(t, monitor.CellAction{Type: "hide", ID: "v1|1|0", Label: "Hide this listing (a dud); keep looking for the thing", Icon: "delete"}, *hide)

	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "hide", ID: "v1|1|0"}))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Blocks[0].Rows, "hidden")
	require.Len(t, res.Events, 1)
	assert.True(t, res.Events[0].Update)
	assert.Equal(t, "Hidden", res.Events[0].Body)
	assert.Equal(t, now, res.Events[0].Until, "its notification ends")
	assert.True(t, res.Background, "nothing left worth seeing")

	// The game's still looked for: a new listing of it notifies.
	eb.items = append(eb.items, listing("2", "nfsu2 good", "30", now, false, 0))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks[0].Rows, 1)
	assert.Equal(t, grail, res.Blocks[0].Rows[0][1].Text)
	assert.Equal(t, []string{"listing/v1|2|0"}, monitortest.Keys(res.Events))
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "hide"}), monitor.ErrBadAction)
}

func TestSections(t *testing.T) {
	eb, gem := newFakeEbay(t), newFakeGemini(t)
	m := newMonitorWith(t, eb.URL, gem.URL, map[string]any{"notify": "uncommon", "owned": []string{"Quake III Arena"},
		"sections": []map[string]any{{
			"title": "Graphics cards", "kind": "gpus", "category": "27386", "name": "a graphics card",
			"queries": []string{"(voodoo, 8800 gtx)", "titan"},
			"owned":   []string{"GeForce 8800 GTX", "GeForce GTX 980"},
			"notify":  "rare",
		}}})
	env := monitortest.Env(now)
	eb.byQuery = map[string][]map[string]any{
		"(voodoo, 8800 gtx)": {listing("g1", "voodoo5 5500 agp", "200", now.Add(-time.Hour), false, 0)},
		"titan":              {},
	}
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)

	for title, j := range map[string][2]string{
		"quake 3 big box":       {"Quake 3 Arena", "common"},
		"ut big box":            {"Unreal Tournament", "uncommon"},
		"voodoo5 5500 agp":      {"Voodoo5 5500", "rare"},
		"xfx 8800 gtx":          {"NVIDIA GeForce 8800 GTX", "rare"},
		"evga 980 ti":           {"GeForce GTX 980 Ti", "uncommon"},
		"gtx titan black boxed": {"GeForce GTX Titan Black", "rare"},
	} {
		gem.names[title], gem.rarity[title] = j[0], j[1]
	}
	eb.items = []map[string]any{listing("1", "quake 3 big box", "20", now, false, 0), listing("2", "ut big box", "30", now, false, 0)}
	eb.byQuery["(voodoo, 8800 gtx)"] = []map[string]any{
		listing("g2", "xfx 8800 gtx", "60", now, false, 0),
		listing("g1", "voodoo5 5500 agp", "200", now.Add(-time.Hour), false, 0),
	}
	eb.byQuery["titan"] = []map[string]any{
		listing("g3", "gtx titan black boxed", "150", now.Add(-time.Minute), false, 0),
		listing("g4", "evga 980 ti", "90", now.Add(-2*time.Minute), false, 0),
		listing("g1", "voodoo5 5500 agp", "200", now.Add(-time.Hour), false, 0),
	}
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks, 2)
	assert.Equal(t, "", res.Blocks[0].Title, "the main search's table")
	assert.Equal(t, "Graphics cards", res.Blocks[1].Title)
	names := func(b monitor.Block) []string {
		var out []string
		for _, r := range b.Rows {
			out = append(out, r[0].Text)
		}
		return out
	}
	assert.Equal(t, []string{"Unreal Tournament"}, names(res.Blocks[0]), "Quake 3 Arena is owned (as Quake III Arena)")
	assert.Equal(t, []string{"GeForce GTX Titan Black", "GeForce GTX 980 Ti", "Voodoo5 5500"}, names(res.Blocks[1]),
		"both queries, once each, newest first; the 8800 GTX is owned, the 980 Ti isn't the 980")
	assert.Equal(t, []string{"listing/v1|2|0", "listing/v1|g3|0"}, monitortest.Keys(res.Events),
		"new listings at each section's notify (uncommon for games, rare for cards); the Voodoo was there at the first check")
	assert.Len(t, res.Summary, 4, "up to three a section")

	var gpuPrompts, gamePrompts int
	for _, sys := range gem.systems {
		switch {
		case strings.Contains(sys, "graphics cards"):
			gpuPrompts++
		case strings.Contains(sys, "Ultima VII"):
			gamePrompts++
		}
	}
	assert.Equal(t, 2, gpuPrompts, "graphics cards are named with their own prompt")
	assert.Equal(t, 1, gamePrompts)
	for _, q := range eb.queries {
		if strings.Contains(q, "titan") {
			assert.Contains(t, q, "category_ids=27386")
		}
	}
}

func TestSectionConfig(t *testing.T) {
	bad := []map[string]any{
		{"queries": []string{"titan"}},
		{"title": "GPUs"},
		{"title": "GPUs", "queries": []string{"titan"}, "kind": "cars"},
		{"title": "GPUs", "queries": []string{"titan"}, "notify": "mythic"},
	}
	for _, sc := range bad {
		c, _ := json.Marshal(map[string]any{"query": "big box", "clientId": "id", "clientSecret": "secret", "sections": []any{sc}})
		_, err := New(c)
		assert.Error(t, err, sc)
	}
}

func TestLoose(t *testing.T) {
	for _, c := range []struct {
		pattern, name string
		want          bool
	}{
		{"Quake III Arena", "Quake 3 Arena", true},
		{"Final Fantasy VII", "Final Fantasy 7", true},
		{"Final Fantasy VII", "Final Fantasy VIII", false},
		{"GeForce 8800 GTX", "NVIDIA GeForce 8800 GTX", true},
		{"GeForce 8800 GT", "GeForce 8800 GTX", false},
		{"GeForce GTX 980", "GeForce GTX 980 Ti", false},
		{"Star Wars: Episode I: Racer", "Star Wars Episode I Racer", true},
		{"Baldur's Gate II: Shadows of Amn", "Baldur's Gate 2: Shadows of Amn", true},
	} {
		assert.Equal(t, c.want, nameMatches(c.pattern, key(c.name)), "%s ~ %s", c.pattern, c.name)
	}
}
