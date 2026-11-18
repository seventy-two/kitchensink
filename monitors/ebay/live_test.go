package ebay

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
