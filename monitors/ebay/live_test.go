package ebay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/seventy-two/kitchensink/ai"
)

// TestLiveCompare names and rates eBay's newest listings with Gemini and
// with Claude, the same prompt each, and lists where they differ. It runs
// only with EBAY_LIVE_COMPARE set, and EBAY_CLIENT_ID, EBAY_CLIENT_SECRET,
// GEMINI_API_KEY and ANTHROPIC_API_KEY. EBAY_LIVE_LIMIT is how many
// listings (default 200); EBAY_LIVE_GEMINI and EBAY_LIVE_CLAUDE pick the
// models.
func TestLiveCompare(t *testing.T) {
	if os.Getenv("EBAY_LIVE_COMPARE") == "" {
		t.Skip("EBAY_LIVE_COMPARE not set")
	}
	limit := 200
	fmt.Sscan(os.Getenv("EBAY_LIVE_LIMIT"), &limit)
	cfg, _ := json.Marshal(map[string]any{
		"searchURL":    "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=1249",
		"clientId":     os.Getenv("EBAY_CLIENT_ID"),
		"clientSecret": os.Getenv("EBAY_CLIENT_SECRET"),
		"name":         "a boxed PC game",
		"limit":        limit,
		"gemini":       map[string]any{"apiKey": os.Getenv("GEMINI_API_KEY")},
	})
	m, err := New(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	items, err := m.search(ctx, http.DefaultClient, time.Now())
	require.NoError(t, err)
	t.Logf("%d listings", len(items))

	type run struct {
		name  string
		got   map[string]judged
		took  time.Duration
		fails int
	}
	ask := func(provider, model string) run {
		require.NoError(t, ai.Shared.Set("ebay", "provider", provider))
		require.NoError(t, ai.Shared.Set("ebay", provider, model))
		r := run{name: model, got: map[string]judged{}}
		start := time.Now()
		for i := 0; i < len(items); i += 50 {
			got, err := m.gemini.judge(ctx, http.DefaultClient, "a boxed PC game", items[i:min(i+50, len(items))])
			if err != nil {
				t.Logf("%s, batch %d: %v", model, i/50, err)
				r.fails++
			}
			for id, j := range got {
				r.got[id] = j
			}
		}
		r.took = time.Since(start)
		return r
	}
	g := ask("gemini", cmpOr(os.Getenv("EBAY_LIVE_GEMINI"), "gemini-3.8-flash"))
	c := ask("claude", cmpOr(os.Getenv("EBAY_LIVE_CLAUDE"), "claude-haiku-5-5"))

	count := func(r run) map[string]int {
		n := map[string]int{}
		for _, j := range r.got {
			n[cmpOr(j.Rarity, "none")]++
		}
		return n
	}
	t.Logf("%-20s answered %d/%d in %s, %d failed batches, %v", g.name, len(g.got), len(items), g.took.Round(time.Second), g.fails, count(g))
	t.Logf("%-20s answered %d/%d in %s, %d failed batches, %v", c.name, len(c.got), len(items), c.took.Round(time.Second), c.fails, count(c))

	var tierDiffs, nameDiffs int
	var lines []string
	for _, it := range items {
		a, b := g.got[it.ItemID], c.got[it.ItemID]
		sameTier, sameName := a.Rarity == b.Rarity, key(a.Name) == key(b.Name)
		if !sameTier {
			tierDiffs++
		}
		if !sameName {
			nameDiffs++
		}
		if !sameTier || !sameName {
			lines = append(lines, fmt.Sprintf("%q\n    gemini: %-9s %s\n    claude: %-9s %s", it.Title, a.Rarity, a.Name, b.Rarity, b.Name))
		}
	}
	t.Logf("rarity differs on %d, name on %d, of %d:\n%s", tierDiffs, nameDiffs, len(items), strings.Join(lines, "\n"))
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

const priceInstruction = `You value eBay listings of boxed PC games for a collector in the UK. For each listing, from its title alone, say what it would typically sell for on eBay UK today, in pounds, not counting postage: the game as described (complete, sealed, disc only, manual only, a lot of several), in the condition the title suggests, used and in good order if it doesn't say. Go by what such items actually sell for, not by what sellers ask. Give a fair range, low to high, that a well-informed buyer would be content to pay, and how sure you are: "low" if you know little of what this sells for.

For each give:
- name: the game as it's properly called, short.
- low, high: the fair range in whole pounds.
- sure: "low", "medium" or "high".`

var priceSchema = map[string]any{
	"type": "array",
	"items": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":   map[string]any{"type": "string"},
			"name": map[string]any{"type": "string"},
			"low":  map[string]any{"type": "number"},
			"high": map[string]any{"type": "number"},
			"sure": map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}},
		},
		"required": []string{"id", "name", "low", "high", "sure"},
	},
}

type valuation struct {
	ID, Name, Sure string
	Low, High      float64
}

// anchors are games the user knows the going price of (18 Nov 2026),
// asked about among the real listings, priced only here.
var anchors = []struct{ title, user string }{
	{"Final Fantasy VII PC Big Box Eidos Complete with Manual", "~£30 (paid £40, ~£10 over)"},
	{"Final Fantasy VIII PC CD-ROM Big Box Edition Complete", "paid £25"},
	{"Harvester PC CD-ROM Big Box 1996 Complete", "~£150"},
	{"Worms Armageddon PC CD-ROM Big Box Team17 Complete", "~£50"},
	{"Need for Speed Underground 2 PC DVD Big Box Complete EA", "£127 is too much"},
}

// TestLivePrices asks Gemini and Claude, blind, what each of eBay's
// newest listings should sell for, and compares that with what's asked
// (price plus postage, Buy It Now in pounds only). It runs only with
// EBAY_LIVE_PRICES set, and the keys TestLiveCompare needs.
func TestLivePrices(t *testing.T) {
	if os.Getenv("EBAY_LIVE_PRICES") == "" {
		t.Skip("EBAY_LIVE_PRICES not set")
	}
	limit := 200
	fmt.Sscan(os.Getenv("EBAY_LIVE_LIMIT"), &limit)
	cfg, _ := json.Marshal(map[string]any{
		"searchURL":    "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=1249",
		"clientId":     os.Getenv("EBAY_CLIENT_ID"),
		"clientSecret": os.Getenv("EBAY_CLIENT_SECRET"),
		"limit":        limit,
		"gemini":       map[string]any{"apiKey": os.Getenv("GEMINI_API_KEY")},
	})
	m, err := New(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	items, err := m.search(ctx, http.DefaultClient, time.Now())
	require.NoError(t, err)
	for i, a := range anchors {
		items = append(items, item{ItemID: fmt.Sprintf("anchor-%d", i), Title: a.title})
	}

	type in struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	ask := func(provider, model string) map[string]valuation {
		require.NoError(t, ai.Shared.Set("ebay", "provider", provider))
		require.NoError(t, ai.Shared.Set("ebay", provider, model))
		out := map[string]valuation{}
		start := time.Now()
		for i := 0; i < len(items); i += 50 {
			var list []in
			for _, it := range items[i:min(i+50, len(items))] {
				list = append(list, in{it.ItemID, it.Title})
			}
			input, _ := json.Marshal(list)
			var got []valuation
			err := m.gemini.ai.Ask(ctx, http.DefaultClient, ai.Request{
				System: priceInstruction, Text: "Value each listing, by its id:\n" + string(input),
				Schema: priceSchema, MaxTokens: 12000,
			}, &got)
			if err != nil {
				t.Logf("%s, batch %d: %v", model, i/50, err)
			}
			for _, v := range got {
				out[v.ID] = v
			}
		}
		t.Logf("%s valued %d/%d in %s", model, len(out), len(items), time.Since(start).Round(time.Second))
		return out
	}
	gName, cName := cmpOr(os.Getenv("EBAY_LIVE_GEMINI"), "gemini-3.8-flash"), cmpOr(os.Getenv("EBAY_LIVE_CLAUDE"), "claude-haiku-5-5")
	g, c := ask("gemini", gName), ask("claude", cName)

	range_ := func(v valuation, ok bool) string {
		if !ok {
			return "—"
		}
		return fmt.Sprintf("£%.0f–%.0f (%s)", v.Low, v.High, v.Sure)
	}
	var b strings.Builder
	b.WriteString("\nYour reference games:\n")
	for i, a := range anchors {
		id := fmt.Sprintf("anchor-%d", i)
		gv, gok := g[id]
		cv, cok := c[id]
		fmt.Fprintf(&b, "  %-58s you: %-28s gemini: %-20s claude: %s\n", a.title, a.user, range_(gv, gok), range_(cv, cok))
	}

	// verdict says how the asking total sits against a range.
	verdict := func(total float64, v valuation, ok bool) string {
		switch {
		case !ok:
			return "?"
		case total < v.Low:
			return "good"
		case total > v.High:
			return "bad"
		}
		return "fair"
	}
	counts := map[string]map[string]int{"gemini": {}, "claude": {}}
	var agree, judged int
	var ratiosG, ratiosC []float64
	b.WriteString("\nReal listings (Buy It Now, pounds; asking = price + postage):\n")
	for _, it := range items {
		if strings.HasPrefix(it.ItemID, "anchor-") || it.auction() || it.Price == nil || it.Price.Currency != "GBP" {
			continue
		}
		price, _ := strconv.ParseFloat(it.Price.Value, 64)
		total := price
		if p := it.postage(); p != nil && p.Currency == "GBP" {
			post, _ := strconv.ParseFloat(p.Value, 64)
			total += post
		}
		gv, gok := g[it.ItemID]
		cv, cok := c[it.ItemID]
		gVer, cVer := verdict(total, gv, gok), verdict(total, cv, cok)
		counts["gemini"][gVer]++
		counts["claude"][cVer]++
		if gok && cok {
			judged++
			if gVer == cVer {
				agree++
			}
			ratiosG = append(ratiosG, total/((gv.Low+gv.High)/2))
			ratiosC = append(ratiosC, total/((cv.Low+cv.High)/2))
		}
		fmt.Fprintf(&b, "  £%7.2f  %-5s %-5s  %-45.45s  gemini %-20s claude %s\n", total, gVer, cVer, it.Title, range_(gv, gok), range_(cv, cok))
	}
	median := func(xs []float64) float64 {
		if len(xs) == 0 {
			return 0
		}
		s := slices.Clone(xs)
		slices.Sort(s)
		return s[len(s)/2]
	}
	fmt.Fprintf(&b, "\nverdicts: gemini %v, claude %v; same verdict on %d of %d\n", counts["gemini"], counts["claude"], agree, judged)
	fmt.Fprintf(&b, "median asking ÷ estimate midpoint: gemini %.2f, claude %.2f\n", median(ratiosG), median(ratiosC))
	t.Log(b.String())
}
