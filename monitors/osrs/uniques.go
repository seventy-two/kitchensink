package osrs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// Uniques are the collection log items the player gets from the tracked
// bosses (and the config's commons, like Vorkath's bones), whatever
// they're worth: every one Dink reports, plus past ones
// imported with "importUniques" (e.g. from RuneLite's Loot Logger). With
// publishUniques set they're written there for petsheet's est. profit.
// Which items are in the collection log comes from TempleOSRS's list,
// fetched once a day.

const (
	// uniquesKey holds every unique drop, oldest first.
	uniquesKey = "uniques"
	// logItemsKey holds the collection log's item IDs and when they were
	// fetched.
	logItemsKey = "logItems"
	// defaultLogItems is TempleOSRS's list of collection log items by
	// category.
	defaultLogItems = "https://templeosrs.com/api/collection-log/categories.php"
	logItemsEvery   = 24 * time.Hour
)

// unique is one collection log item from a drop.
type unique struct {
	Time   time.Time `json:"time"`
	Metric string    `json:"metric"`
	Source string    `json:"source,omitempty"`
	ItemID int       `json:"id"`
	Item   string    `json:"item"`
	Qty    int64     `json:"qty"`
	// Price is one's price when it dropped, 0 when it had none (an
	// untradeable, or a new item before the GE priced it).
	Price int64 `json:"price"`
	KC    int64 `json:"kc,omitempty"`
}

// logItems is the collection log's item IDs.
type logItems struct {
	Fetched time.Time    `json:"fetched"`
	IDs     map[int]bool `json:"ids"`
}

// refreshLogItems fetches the collection log's items when the stored
// list is a day old. A failure keeps the old list: the drops it misses
// can be imported later.
func (m *Monitor) refreshLogItems(ctx context.Context, env monitor.Env) {
	var have logItems
	if _, err := env.State.Get(logItemsKey, &have); err != nil {
		return
	}
	if len(have.IDs) > 0 && env.Now().Sub(have.Fetched) < logItemsEvery {
		return
	}
	ids, err := fetchLogItems(ctx, env.HTTP, m.cfg.LogItemsURL)
	if err != nil {
		if env.Log != nil {
			env.Log.Warn("osrs: collection log items", "err", err)
		}
		return
	}
	env.State.Set(logItemsKey, logItems{Fetched: env.Now(), IDs: ids})
}

func fetchLogItems(ctx context.Context, client *http.Client, address string) (map[int]bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", address, resp.Status)
	}
	var groups map[string]map[string][]int
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&groups); err != nil {
		return nil, err
	}
	ids := map[int]bool{}
	for _, categories := range groups {
		for _, items := range categories {
			for _, id := range items {
				ids[id] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s: no items", address)
	}
	return ids, nil
}

// recordUniques keeps the collection log items in a drop from a tracked
// metric, and the config's commons for it. Priced ones are added to the
// day's record of the same item (see merge).
func (m *Monitor) recordUniques(env monitor.Env, at time.Time, metric, source string, kc int64, items []dinkItem) error {
	var known logItems
	if _, err := env.State.Get(logItemsKey, &known); err != nil {
		return err
	}
	var all []unique
	if _, err := env.State.Get(uniquesKey, &all); err != nil {
		return err
	}
	changed := false
	for _, it := range items {
		// A noted item's ID is the item's plus one (Mole claw 7417 is
		// 7416 noted); the log lists the unnoted one.
		if !known.IDs[it.ID] && known.IDs[it.ID-1] {
			it.ID--
		}
		if it.Quantity <= 0 || !(known.IDs[it.ID] || slices.Contains(m.cfg.Commons[metric], it.ID)) {
			continue
		}
		all = merge(all, unique{Time: at, Metric: metric, Source: source, ItemID: it.ID, Item: it.Name, Qty: it.Quantity, Price: it.PriceEach, KC: kc})
		changed = true
	}
	if !changed {
		return nil
	}
	return env.State.Set(uniquesKey, all)
}

// priced reports whether a unique has a usable price: not 0 (untradeable,
// or unpriced yet), nor Loot Logger's int32 cap.
func priced(u unique) bool { return u.Price > 0 && u.Price < 1<<31-1 }

// sameDay reports whether two uniques are the same item from the same
// metric on the same (UTC) day.
func sameDay(a, b unique) bool {
	return a.Metric == b.Metric && a.ItemID == b.ItemID && a.Time.UTC().Format(time.DateOnly) == b.Time.UTC().Format(time.DateOnly)
}

// merge adds u to all. A priced item goes into that day's record of it
// (quantities added, the price averaged by quantity), which keeps the
// stacks every kill gives (Zulrah's scales, mole skins) to one record a
// day; one without a price stays a drop of its own, since petsheet
// prices it by its day and matches it to big drops by time.
func merge(all []unique, u unique) []unique {
	if priced(u) {
		for i := len(all) - 1; i >= 0; i-- {
			if o := &all[i]; priced(*o) && sameDay(*o, u) {
				o.Price = (o.Price*o.Qty + u.Price*u.Qty) / (o.Qty + u.Qty)
				o.Qty += u.Qty
				o.KC = max(o.KC, u.KC)
				return all
			}
		}
	}
	return append(all, u)
}

// sameDrop is how close in time an import and a drop Dink reported must
// be to count as one: Loot Logger stamps the game client's time, the
// monitor the time Dink's message came.
const sameDrop = 3 * time.Minute

// importUniques adds past uniques, as data [{time, metric or source, id,
// item, qty, price, kc}], priced ones one record a day per item (as
// merge keeps them). A source is matched to its tracked metric; others
// are dropped, as are ones already kept (for priced ones, any of the
// item that day; else the same quantity within sameDrop), so a Loot
// Logger export can be imported again to fill gaps.
func (m *Monitor) importUniques(env monitor.Env, data json.RawMessage) error {
	var in []unique
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	var all []unique
	if _, err := env.State.Get(uniquesKey, &all); err != nil {
		return err
	}
	have := func(u unique) bool {
		for _, o := range all {
			if priced(u) && priced(o) && sameDay(o, u) {
				return true
			}
			if o.Metric == u.Metric && o.ItemID == u.ItemID && o.Qty == u.Qty && o.Time.Sub(u.Time).Abs() <= sameDrop {
				return true
			}
		}
		return false
	}
	for _, u := range in {
		if u.Time.IsZero() || u.ItemID <= 0 || u.Qty <= 0 {
			return fmt.Errorf("%w: a unique needs a time, an item id and a quantity", monitor.ErrBadAction)
		}
		if u.Metric == "" {
			u.Metric, _ = sourceMetric(u.Source)
		}
		if _, ok := tracked[u.Metric]; !ok || have(u) {
			continue
		}
		all = append(all, u)
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	return env.State.Set(uniquesKey, all)
}

// publishUniques writes the uniques to the PublishUniques file, if set,
// by writing a temporary file and renaming it.
func (m *Monitor) publishUniques(env monitor.Env) error {
	if m.cfg.PublishUniques == "" {
		return nil
	}
	out := struct {
		Player  string    `json:"player"`
		Updated time.Time `json:"updated"`
		Uniques []unique  `json:"uniques"`
	}{Player: m.cfg.Player, Updated: env.Now()}
	if _, err := env.State.Get(uniquesKey, &out.Uniques); err != nil {
		return err
	}
	if out.Uniques == nil {
		out.Uniques = []unique{}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return writeFile(m.cfg.PublishUniques, data)
}

// writeFile writes data to file through a temporary file and a rename,
// so a reader never sees half of it.
func writeFile(file string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".osrs-*.json")
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("publish: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
