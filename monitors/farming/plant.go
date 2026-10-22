package farming

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
)

// run is a farm run: the kinds of patch (RuneLite's implementations) done
// together. Plant, if set, picks what to plant from crops with the same
// pet chance, so the cheapest wins.
type run struct {
	ID, Title, Short string
	Impls            []string
	Plant            []crop
}

// runs are the farm runs that count towards Tangleroot, roughly in the
// order the wiki's strategy gives them (Tangleroot/Strategies): quick
// patches a few times a day, then fruit trees, hardwoods and the rest.
// Herbs, allotments and the like give it rarely and aren't shown.
var runs = []run{
	{ID: "quick", Title: "Quick patches", Short: "Quick", Impls: []string{"SEAWEED", "BELLADONNA", "MUSHROOM", "CACTUS"}},
	{ID: "trees", Title: "Trees", Short: "Trees", Impls: []string{"TREE"}},
	{ID: "fruit", Title: "Fruit trees", Short: "Fruit", Impls: []string{"FRUIT_TREE", "CALQUAT"}, Plant: fruitTrees},
	{ID: "hardwood", Title: "Hardwood", Short: "Hardwood", Impls: []string{"HARDWOOD_TREE"}, Plant: hardwoods},
	{ID: "special", Title: "Specials", Short: "Specials", Impls: []string{"CELASTRUS", "REDWOOD", "SPIRIT_TREE", "CRYSTAL_TREE", "HESPORI", "ANIMA"}},
}

func runOf(impl string) int {
	return slices.IndexFunc(runs, func(r run) bool { return slices.Contains(r.Impls, impl) })
}

// implNames name each kind of patch in notifications.
var implNames = map[string]string{
	"SEAWEED": "Giant seaweed", "BELLADONNA": "Belladonna", "MUSHROOM": "Mushrooms", "CACTUS": "Cacti",
	"TREE": "Trees", "FRUIT_TREE": "Fruit trees", "CALQUAT": "Calquats", "HARDWOOD_TREE": "Hardwoods",
	"CELASTRUS": "Celastrus", "REDWOOD": "Redwood", "SPIRIT_TREE": "Spirit trees", "CRYSTAL_TREE": "Crystal tree",
	"HESPORI": "Hespori", "ANIMA": "Anima plant",
}

// patchNames name each kind of patch, for empty ones.
var patchNames = map[string]string{
	"SEAWEED": "Seaweed patch", "BELLADONNA": "Belladonna patch", "MUSHROOM": "Mushroom patch", "CACTUS": "Cactus patch",
	"TREE": "Tree patch", "FRUIT_TREE": "Fruit tree patch", "CALQUAT": "Calquat patch", "HARDWOOD_TREE": "Hardwood patch",
	"CELASTRUS": "Celastrus patch", "REDWOOD": "Redwood patch", "SPIRIT_TREE": "Spirit tree patch", "CRYSTAL_TREE": "Crystal tree patch",
	"HESPORI": "Hespori patch", "ANIMA": "Anima patch",
}

// petBase is each crop's base chance of Tangleroot (1 in base - 25 ×
// Farming level), per check-health, or per final harvest for crops
// without one, from the wiki's Tangleroot page.
var petBase = map[string]int{
	"OAK": 22483, "WILLOW": 16059, "MAPLE": 14052, "YEW": 11242, "MAGIC": 9368,
	"APPLE": 9000, "BANANA": 9000, "ORANGE": 9000, "CURRY": 9000, "PINEAPPLE": 9000,
	"PAPAYA": 9000, "PALM": 9000, "DRAGONFRUIT": 9000,
	"TEAK": 5000, "MAHOGANY": 5000, "CAMPHOR": 5000, "IRONWOOD": 5000, "ROSEWOOD": 5000,
	"CALQUAT": 6000, "CELASTRUS": 9000, "REDWOOD": 5000, "SPIRIT_TREE": 5000, "CRYSTAL_TREE": 9000,
	"HESPORI": 7000, "CACTUS": 7000, "POTATO_CACTUS": 160594, "BELLADONNA": 8000,
	"MUSHROOM": 7500, "SEAWEED": 7500,
}

// petChance is the "1 in N" chance for a crop at a level, or 0 if unknown.
func petChance(produce string, level int) int {
	base := petBase[produce]
	if base == 0 || level == 0 {
		return 0
	}
	return base - 25*level
}

// crop is something to plant: its seed and sapling (either will do; a
// seed needs growing into a sapling first) and what a gardener wants to
// protect it, by GE item ID.
type crop struct {
	Produce       string
	Level         int
	Seed, Sapling int
	Pay           int // how many of PayItem
	PayItem       int
	PayName       string
}

// fruitTrees all have the same pet chance and growth time, so the
// cheapest is best. Payments are from each tree's wiki page.
var fruitTrees = []crop{
	{"APPLE", 27, 5283, 5496, 9, 5986, "sweetcorn"},
	{"BANANA", 33, 5284, 5497, 4, 5386, "baskets of apples"},
	{"ORANGE", 39, 5285, 5498, 3, 5406, "baskets of strawberries"},
	{"CURRY", 42, 5286, 5499, 5, 5416, "baskets of bananas"},
	{"PINEAPPLE", 51, 5287, 5500, 10, 5982, "watermelons"},
	{"PAPAYA", 57, 5288, 5501, 10, 2114, "pineapples"},
	{"PALM", 68, 5289, 5502, 15, 5972, "papayas"},
	{"DRAGONFRUIT", 81, 22877, 22866, 15, 5974, "coconuts"},
}

// hardwoods all have the same pet chance; teak grows a tick faster than
// most and rosewood a tick slower.
var hardwoods = []crop{
	{"TEAK", 35, 21486, 21477, 15, 225, "limpwurt roots"},
	{"MAHOGANY", 55, 21488, 21480, 25, 5998, "Yanillian hops"},
	{"CAMPHOR", 66, 31547, 31502, 10, 239, "white berries"},
	{"IRONWOOD", 80, 31549, 31505, 10, 5970, "curry leaves"},
	{"ROSEWOOD", 92, 31551, 31508, 8, 22929, "dragonfruit"},
}

// choice is a crop with what it costs.
type choice struct {
	crop
	Plant   int // the cheaper of seed and sapling
	Payment int // all of the payment, if protecting
	Total   int
}

// cheapest picks the cheapest crop the player can plant, with its costs,
// or reports false without prices. A level of 0 (unknown) allows all.
func cheapest(crops []crop, prices map[int]int, level int, protect bool) (choice, bool) {
	var best choice
	found := false
	for _, c := range crops {
		if level > 0 && c.Level > level {
			continue
		}
		seed, sapling := prices[c.Seed], prices[c.Sapling]
		if seed == 0 && sapling == 0 {
			continue
		}
		ch := choice{crop: c, Plant: seed}
		if ch.Plant == 0 || (sapling > 0 && sapling < ch.Plant) {
			ch.Plant = sapling
		}
		if protect {
			if prices[c.PayItem] == 0 {
				continue
			}
			ch.Payment = c.Pay * prices[c.PayItem]
		}
		ch.Total = ch.Plant + ch.Payment
		if !found || ch.Total < best.Total {
			best, found = ch, true
		}
	}
	return best, found
}

// fetchPrices gets the latest GE prices of the crops and payments, from
// the OSRS Wiki's real-time prices: the price it last sold at instantly
// (high), what buying it now costs.
func (m *Monitor) fetchPrices(ctx context.Context, client *http.Client) (map[int]int, error) {
	var body struct {
		Data map[string]struct {
			High int `json:"high"`
			Low  int `json:"low"`
		} `json:"data"`
	}
	if err := getJSON(ctx, client, m.cfg.PricesURL+"/latest", &body); err != nil {
		return nil, err
	}
	out := map[int]int{}
	for _, list := range [][]crop{fruitTrees, hardwoods} {
		for _, c := range list {
			for _, id := range []int{c.Seed, c.Sapling, c.PayItem} {
				p := body.Data[strconv.Itoa(id)]
				out[id] = max(p.High, p.Low)
			}
		}
	}
	return out, nil
}

// fetchLevel gets the player's Farming level from Wise Old Man.
func (m *Monitor) fetchLevel(ctx context.Context, client *http.Client) (int, error) {
	var body struct {
		LatestSnapshot struct {
			Data struct {
				Skills map[string]struct {
					Level int `json:"level"`
				} `json:"skills"`
			} `json:"data"`
		} `json:"latestSnapshot"`
	}
	if err := getJSON(ctx, client, m.cfg.WOMURL+"/players/"+url.PathEscape(m.cfg.Player), &body); err != nil {
		return 0, err
	}
	return body.LatestSnapshot.Data.Skills["farming"].Level, nil
}

func getJSON(ctx context.Context, client *http.Client, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	// Both APIs ask for a User-Agent that says who's asking.
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("%s: %s", u, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}
