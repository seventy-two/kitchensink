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
		json.NewEncoder(w).Encode(map[string]any{"itemSummaries": f.items})
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
	mu    sync.Mutex
	names map[string]string // by title
	asked [][]string
	fail  bool
}

func newFakeGemini(t *testing.T) *fakeGemini {
	g := &fakeGemini{names: map[string]string{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input string }
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		var list []struct{ ID, Title string }
		json.Unmarshal([]byte(req.Input[strings.Index(req.Input, "["):]), &list)
		var titles []string
		var out []map[string]string
		for _, l := range list {
			titles = append(titles, l.Title)
			if n, ok := g.names[l.Title]; ok {
				out = append(out, map[string]string{"id": l.ID, "name": n})
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
	raw, _ := json.Marshal(map[string]any{
		"searchURL":    "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=0&_from=R40&_sop=10",
		"clientId":     "id",
		"clientSecret": "secret",
		"name":         "a boxed PC game",
		"apiURL":       api,
		"gemini":       map[string]any{"apiKey": "k", "url": gem},
	})
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
	assert.Equal(t, []string{"listing/v1|2|0", "listing/v1|1|0"}, monitortest.Keys(res.Events))
	for _, e := range res.Events {
		assert.True(t, e.Quiet, "listings already up at the first check aren't new")
	}
	assert.Contains(t, eb.queries[0], "q=big+box+pc")
	assert.Contains(t, eb.queries[0], "sort=newlyListed")
	assert.NotContains(t, eb.queries[0], "category_ids", "_sacat=0 is all categories")

	rows := res.Blocks[0].Rows
	require.Len(t, rows, 2)
	assert.Equal(t, "SimCity 2000", rows[0][0].Text)
	assert.Equal(t, "https://www.ebay.co.uk/itm/2", rows[0][0].URL)
	assert.Equal(t, "£24.99", rows[0][1].Text)
	assert.Equal(t, "", rows[0][1].Detail)
	assert.Equal(t, "19:30", rows[0][2].Text)
	assert.Equal(t, "Ultima VII: The Black Gate", rows[1][0].Text)
	assert.Equal(t, "£45", rows[1][1].Text)
	assert.Equal(t, "3 bids", rows[1][1].Detail)
	assert.Equal(t, "Mon 19:30", rows[1][2].Text)
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
	assert.Equal(t, "auction", res.Blocks[0].Rows[0][1].Detail)

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
