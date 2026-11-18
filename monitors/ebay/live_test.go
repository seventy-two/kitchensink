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

// priced are the user's reference games at a price they'd call fair and
// one they wouldn't, to see whether a verdict follows the price.
var priced = []struct {
	title string
	price float64
	user  string
}{
	{"Final Fantasy VII PC Big Box Eidos Complete with Manual", 30, "fair (~£30)"},
	{"Final Fantasy VII PC Big Box Eidos Complete with Manual", 55, "overpriced"},
	{"Final Fantasy VIII PC CD-ROM Big Box Edition Complete", 25, "fair (paid £25)"},
	{"Final Fantasy VIII PC CD-ROM Big Box Edition Complete", 60, "overpriced"},
	{"Harvester PC CD-ROM Big Box 1996 Complete", 150, "fair (~£150)"},
	{"Harvester PC CD-ROM Big Box 1996 Complete", 70, "good"},
	{"Worms Armageddon PC CD-ROM Big Box Team17 Complete", 50, "fair (~£50)"},
	{"Worms Armageddon PC CD-ROM Big Box Team17 Complete", 110, "overpriced"},
	{"Need for Speed Underground 2 PC DVD Big Box Complete EA", 127, "overpriced"},
}

// TestLiveDeals tries three ways for Gemini to judge prices: blind with
// Google Search (A), told the asking price without search (B) and with it
// (C), on the user's reference games and on the listings the monitor
// would judge (uncommon, rare or grail, and the dearest). It runs only
// with EBAY_LIVE_DEALS set, and the keys TestLiveCompare needs.
func TestLiveDeals(t *testing.T) {
	if os.Getenv("EBAY_LIVE_DEALS") == "" {
		t.Skip("EBAY_LIVE_DEALS not set")
	}
	cfg, _ := json.Marshal(map[string]any{
		"searchURL":    "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=1249",
		"clientId":     os.Getenv("EBAY_CLIENT_ID"),
		"clientSecret": os.Getenv("EBAY_CLIENT_SECRET"),
		"limit":        200,
		"grails":       []string{"Wipeout", "Metal Gear Solid*", "Need for Speed: Underground", "Need for Speed: Underground 2", "Croc", "Croc: Legend of the Gobbos", "Croc 2", "Sabrina the Teenage Witch: Brat Attack", "Harvester", "Worms Armageddon", "Black & White"},
		"gemini":       map[string]any{"apiKey": os.Getenv("GEMINI_API_KEY")},
	})
	m, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, ai.Shared.Set("ebay", "provider", "gemini"))
	ctx := context.Background()
	items, err := m.search(ctx, http.DefaultClient, time.Now())
	require.NoError(t, err)

	// Buy It Now in pounds, with what's asked including postage.
	asking := map[string]float64{}
	var bin []item
	for _, it := range items {
		if it.auction() || it.Price == nil || it.Price.Currency != "GBP" {
			continue
		}
		p, _ := strconv.ParseFloat(it.Price.Value, 64)
		if pp := it.postage(); pp != nil && pp.Currency == "GBP" {
			v, _ := strconv.ParseFloat(pp.Value, 64)
			p += v
		}
		asking[it.ItemID] = p
		bin = append(bin, it)
	}
	// Which the monitor would judge: uncommon, rare or grail.
	tierOf := map[string]string{}
	for i := 0; i < len(bin); i += 50 {
		got, err := m.gemini.judge(ctx, http.DefaultClient, "a boxed PC game", bin[i:min(i+50, len(bin))])
		require.NoError(t, err)
		for id, j := range got {
			tierOf[id] = j.Rarity
			if m.grail(j.Name) {
				tierOf[id] = grail
			}
		}
	}
	var sample []item
	for _, it := range bin {
		if rank(tierOf[it.ItemID]) > 0 {
			sample = append(sample, it)
		}
	}
	notable := len(sample)
	dear := slices.Clone(bin)
	slices.SortFunc(dear, func(a, b item) int { return int(asking[b.ItemID]*100 - asking[a.ItemID]*100) })
	for _, it := range dear {
		if len(sample) >= notable+10 {
			break
		}
		if rank(tierOf[it.ItemID]) <= 0 {
			sample = append(sample, it)
		}
	}
	type entry struct {
		id, title string
		price     float64
	}
	var entries []entry
	for i, p := range priced {
		entries = append(entries, entry{fmt.Sprintf("ref-%d", i), p.title, p.price})
	}
	for _, it := range sample {
		entries = append(entries, entry{it.ItemID, it.Title, asking[it.ItemID]})
	}
	t.Logf("%d reference prices, %d notable listings, %d dearest others", len(priced), notable, len(sample)-notable)

	type inBlind struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	type inPriced struct {
		ID     string  `json:"id"`
		Title  string  `json:"title"`
		Asking float64 `json:"asking"`
	}
	run := func(name string, blind, search bool) map[string]deal {
		out := map[string]deal{}
		start := time.Now()
		// Few at a time with search, so each gets looked up.
		size := 40
		if search {
			size = 8
		}
		for i := 0; i < len(entries); i += size {
			batch := entries[i:min(i+size, len(entries))]
			var input []byte
			if blind {
				var l []inBlind
				seen := map[string]bool{}
				for _, e := range batch {
					if !seen[e.title] { // the reference games appear twice
						l = append(l, inBlind{e.id, e.title})
					}
					seen[e.title] = true
				}
				input, _ = json.Marshal(l)
				var got []valuation
				err := m.gemini.ai.Ask(ctx, http.DefaultClient, ai.Request{
					System: priceInstruction, Text: "Value each listing, by its id:\n" + string(input),
					Schema: priceSchema, MaxTokens: 12000, Search: search,
				}, &got)
				if err != nil {
					t.Logf("%s, batch %d: %v", name, i/size, err)
				}
				for _, v := range got {
					out[v.ID] = deal{ID: v.ID, Low: v.Low, High: v.High, Why: v.Sure}
				}
				continue
			}
			var l []inPriced
			for _, e := range batch {
				l = append(l, inPriced{e.id, e.title, e.price})
			}
			input, _ = json.Marshal(l)
			var got []deal
			err := m.gemini.ai.Ask(ctx, http.DefaultClient, ai.Request{
				System: dealInstruction, Text: "Judge each listing's asking price (in pounds, postage included), by its id:\n" + string(input),
				Schema: dealSchema, MaxTokens: 12000, Search: search,
			}, &got)
			if err != nil {
				t.Logf("%s, batch %d: %v", name, i/size, err)
			}
			for _, d := range got {
				out[d.ID] = d
			}
		}
		t.Logf("%s answered %d/%d in %s", name, len(out), len(entries), time.Since(start).Round(time.Second))
		return out
	}
	a := run("A blind+search", true, true)
	b := run("B priced", false, false)
	c := run("C priced+search", false, true)

	// A blind range gives a verdict the same way as the deal test.
	blindVerdict := func(e entry) (string, string) {
		id := e.id
		if strings.HasPrefix(id, "ref-") { // asked once per title
			for _, x := range entries {
				if x.title == e.title {
					id = x.id
					break
				}
			}
		}
		v, ok := a[id]
		if !ok {
			return "?", "—"
		}
		r := fmt.Sprintf("£%.0f–%.0f", v.Low, v.High)
		switch {
		case e.price < v.Low:
			return "good", r
		case e.price > v.High:
			return "overpriced", r
		}
		return "fair", r
	}
	cell := func(d deal, ok bool) string {
		if !ok {
			return "?"
		}
		return fmt.Sprintf("%-10s £%.0f–%.0f", d.Verdict, d.Low, d.High)
	}
	var w strings.Builder
	w.WriteString("\nReference games (asking → you | A blind+search | B priced | C priced+search):\n")
	right := map[string]int{}
	for i, p := range priced {
		e := entries[i]
		av, ar := blindVerdict(e)
		bd, bok := b[e.id]
		cd, cok := c[e.id]
		want := strings.Fields(p.user)[0]
		for k, v := range map[string]string{"A": av, "B": bd.Verdict, "C": cd.Verdict} {
			if v == want {
				right[k]++
			}
		}
		fmt.Fprintf(&w, "  %-28.28s £%4.0f → %-16s | %-10s %-10s | %s | %s\n", p.title, p.price, p.user, av, ar, cell(bd, bok), cell(cd, cok))
	}
	fmt.Fprintf(&w, "  right of %d: A %d, B %d, C %d\n", len(priced), right["A"], right["B"], right["C"])
	w.WriteString("\nListings (tier, asking | A | B | C, with C's reason):\n")
	agreeBC := 0
	for _, e := range entries[len(priced):] {
		av, ar := blindVerdict(e)
		bd, bok := b[e.id]
		cd, cok := c[e.id]
		if bd.Verdict == cd.Verdict {
			agreeBC++
		}
		var title string
		for _, it := range sample {
			if it.ItemID == e.id {
				title = it.Title
			}
		}
		fmt.Fprintf(&w, "  %-8s £%6.2f %-40.40s | %-10s %-10s | %s | %s · %s\n", tierOf[e.id], e.price, title, av, ar, cell(bd, bok), cell(cd, cok), cd.Why)
	}
	fmt.Fprintf(&w, "B and C agree on %d of %d listings\n", agreeBC, len(entries)-len(priced))
	t.Log(w.String())
}
