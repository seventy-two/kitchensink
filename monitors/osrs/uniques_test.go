package osrs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUniques(t *testing.T) {
	f := newFakeWOM(t)
	temple := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"bosses":{"vorkath":[21992,22006,22111,11286]},"clues":{"third_age":[20011]}}`))
	}))
	t.Cleanup(temple.Close)
	file := filepath.Join(t.TempDir(), "uniques.json")
	raw, _ := json.Marshal(map[string]any{"player": "GTX 480", "womURL": f.URL, "hookSecret": "s3cret", "publishUniques": file, "logItemsURL": temple.URL, "commons": map[string][]int{"vorkath": {22124}}})
	m, err := New(raw)
	require.NoError(t, err)
	env := monitortest.Env(now)

	// The first poll fetches the collection log's items.
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)

	// A necklace under any loot threshold is kept, and the superior bones
	// (a common); dragon bones aren't in the log. A second kill's bones
	// go into the day's record.
	hook(t, m, env, "LOOT", `{"source":"Vorkath","category":"NPC","killCount":117,"items":[
		{"id":22111,"name":"Dragonbone necklace","quantity":1,"priceEach":100000},
		{"id":22124,"name":"Superior dragon bones","quantity":2,"priceEach":10000},
		{"id":22112,"name":"Dragonbone necklace","quantity":1,"priceEach":100000},
		{"id":536,"name":"Dragon bones","quantity":2,"priceEach":3000}]}`)
	hook(t, m, env, "LOOT", `{"source":"Vorkath","category":"NPC","killCount":118,"items":[
		{"id":22124,"name":"Superior dragon bones","quantity":2,"priceEach":12000}]}`)

	// Loot Logger's copy of the same drop (a minute off) is skipped; an
	// older one, and a 3rd age axe from a master clue, are added; a drop
	// from an untracked source isn't.
	imported := `[
		{"time":"` + now.Add(-time.Minute).Format(time.RFC3339) + `","source":"Vorkath","id":22111,"item":"Dragonbone necklace","qty":1,"price":90000},
		{"time":"2025-01-01T12:00:00Z","source":"Vorkath","id":11286,"item":"Draconic visage","qty":1,"price":2000000,"kc":40},
		{"time":"2025-04-28T09:57:05Z","source":"Clue Scroll (Master)","id":20011,"item":"3rd age axe","qty":1,"price":2147745872},
		{"time":"2025-02-01T12:00:00Z","source":"Blue dragon","id":11286,"item":"Draconic visage","qty":1,"price":2000000}
	]`
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "importUniques", Data: json.RawMessage(imported)}))
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "importUniques", Data: json.RawMessage(`[{"source":"Vorkath"}]`)}), monitor.ErrBadAction)

	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var got struct {
		Uniques []unique `json:"uniques"`
	}
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got.Uniques, 4)
	assert.Equal(t, "Draconic visage", got.Uniques[0].Item, "oldest first")
	assert.Equal(t, "clue_scrolls_master", got.Uniques[1].Metric)
	bones := got.Uniques[3]
	assert.Equal(t, int64(4), bones.Qty)
	assert.Equal(t, int64(11000), bones.Price, "averaged")
	assert.Equal(t, int64(118), bones.KC)
	assert.True(t, now.Equal(got.Uniques[2].Time))
	got.Uniques[2].Time = now
	assert.Equal(t, unique{Time: now, Metric: "vorkath", Source: "Vorkath", ItemID: 22111, Item: "Dragonbone necklace", Qty: 2, Price: 100000, KC: 117}, got.Uniques[2], "a noted one (22112) counts as the necklace")
}
