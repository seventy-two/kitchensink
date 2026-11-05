package osrs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// published is what the monitor writes to the config's Publish file: the
// player's latest counts (Wise Old Man's, raised by Dink's) and every drop
// in the history. It holds nothing the hiscores and the player's own
// loot don't already show, so it can be served publicly.
type published struct {
	Player  string    `json:"player"`
	Updated time.Time `json:"updated"`
	// Seen is when Dink last heard from the player.
	Seen  time.Time `json:"seen,omitzero"`
	Stats stats     `json:"stats"`
	Drops []drop    `json:"drops"`
}

// publish writes the Publish file, if there is one, by writing a
// temporary file and renaming it, so a reader never sees half of it.
func (m *Monitor) publish(env monitor.Env, counts stats) error {
	if m.cfg.Publish == "" {
		return nil
	}
	out := published{Player: m.cfg.Player, Updated: env.Now(), Stats: stats{}}
	for id, n := range counts {
		if _, ok := tracked[id]; ok {
			out.Stats[id] = n
		}
	}
	if _, err := env.State.Get(seenKey, &out.Seen); err != nil {
		return err
	}
	if _, err := env.State.Get(historyKey, &out.Drops); err != nil {
		return err
	}
	if out.Drops == nil {
		out.Drops = []drop{}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.cfg.Publish), ".osrs-*.json")
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
	return os.Rename(tmp.Name(), m.cfg.Publish)
}

// importDrops adds past drops to the history, as data [{time, source,
// item, id, value, kc, rarity, approxKC}] (e.g. from RuneLite's Loot
// Logger). Each source is matched to its tracked metric ("Clue Scroll
// (Master)" is master clues); drops from elsewhere are dropped, as are
// ones already in the history (same time and item). The history stays
// oldest first.
func (m *Monitor) importDrops(env monitor.Env, data json.RawMessage) error {
	var in []drop
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	var history []drop
	if _, err := env.State.Get(historyKey, &history); err != nil {
		return err
	}
	have := map[string]bool{}
	dropKey := func(d drop) string { return fmt.Sprintf("%d|%s", d.Time.Unix(), d.Item) }
	for _, d := range history {
		have[dropKey(d)] = true
	}
	for _, d := range in {
		if d.Time.IsZero() || d.Item == "" {
			return fmt.Errorf("%w: a drop needs a time and an item", monitor.ErrBadAction)
		}
		if d.Metric == "" {
			if id, ok := sourceMetric(d.Source); ok {
				d.Metric = id
			}
		}
		if _, ok := tracked[d.Metric]; !ok || have[dropKey(d)] {
			continue
		}
		have[dropKey(d)] = true
		history = append(history, d)
	}
	sort.SliceStable(history, func(i, j int) bool { return history[i].Time.Before(history[j].Time) })
	return env.State.Set(historyKey, history)
}

// sourceMetric is the tracked metric for a loot source as the game or
// Loot Logger names it: a boss, raid or master clue.
func sourceMetric(source string) (string, bool) {
	if s := strings.ToLower(source); strings.Contains(s, "clue") && strings.Contains(s, "master") {
		return "clue_scrolls_master", true
	}
	return metricFor(source)
}
