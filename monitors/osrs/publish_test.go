package osrs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHistoryAndPublish(t *testing.T) {
	f := newFakeWOM(t)
	file := filepath.Join(t.TempDir(), "live.json")
	raw, _ := json.Marshal(map[string]any{"player": "GTX 480", "womURL": f.URL, "hookSecret": "s3cret", "publish": file})
	m, err := New(raw)
	require.NoError(t, err)
	env := monitortest.Env(now)

	// Past drops: a raid numbered by hand, a duplicate, and one from a
	// source with no pet, which is left out.
	imported := `[
		{"time":"2025-04-28T09:57:05Z","source":"Clue Scroll (Master)","item":"3rd age axe","id":12426,"value":2147483647,"kc":95},
		{"time":"2024-05-11T11:23:11Z","source":"Chambers of Xeric","item":"Dexterous prayer scroll","id":21034,"value":17288073,"kc":12,"approxKC":true},
		{"time":"2024-05-11T11:23:11Z","source":"Chambers of Xeric","item":"Dexterous prayer scroll","id":21034,"value":17288073,"kc":12,"approxKC":true},
		{"time":"2024-01-01T00:00:00Z","source":"Demonic gorilla","item":"Zenyte shard","value":12000000}
	]`
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: "importDrops", Data: json.RawMessage(imported)}))
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: "importDrops", Data: json.RawMessage(`[{"item":"x"}]`)}), monitor.ErrBadAction)

	// Live: 15m is history but not big enough for today's panel; 5m is neither.
	hook(t, m, env, "LOOT", `{"source":"Vorkath","category":"NPC","killCount":115,"items":[
		{"id":22006,"name":"Skeletal visage","quantity":1,"priceEach":15000000,"rarity":0.0002},
		{"id":536,"name":"Dragon bones","quantity":2,"priceEach":3000}]}`)
	hook(t, m, env, "LOOT", `{"source":"Vorkath","category":"NPC","killCount":116,"items":[{"id":1,"name":"Dragonbone necklace","quantity":1,"priceEach":5000000}]}`)
	hook(t, m, env, "PET", `{"petName":"Vorki","duplicate":false}`)
	// Combat achievements (Dink's example shape); a repeat is kept once.
	ca := `{"tier":"MASTER","task":"Duke Sucellus Master","taskPoints":5,"totalPoints":1841,"tierProgress":0,"tierTotalPoints":865,"totalPossiblePoints":2697,"currentTier":"ELITE","nextTier":"MASTER"}`
	hook(t, m, env, "COMBAT_ACHIEVEMENT", ca)
	hook(t, m, env, "COMBAT_ACHIEVEMENT", ca)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks, 2, "the visage is under bigLoot, so only the pet is in today's table")

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var got published
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(116), got.Stats["vorkath"])
	assert.Equal(t, int64(42), got.Stats["clue_scrolls_master"])
	_, attack := got.Stats["attack"]
	assert.False(t, attack, "only metrics with a pet")
	require.Len(t, got.Drops, 4)
	assert.Equal(t, "chambers_of_xeric", got.Drops[0].Metric, "oldest first")
	assert.True(t, got.Drops[0].ApproxKC)
	assert.Equal(t, "clue_scrolls_master", got.Drops[1].Metric)
	assert.True(t, now.Equal(got.Drops[2].Time))
	got.Drops[2].Time = now
	assert.Equal(t, drop{Time: now, Source: "Vorkath", Item: "Skeletal visage", Value: 15_006_000, KC: 115,
		ItemID: 22006, Rarity: 0.0002, Metric: "vorkath"}, got.Drops[2])
	assert.True(t, got.Drops[3].Pet)
	assert.Equal(t, "vorkath", got.Drops[3].Metric, "the pet is put down to the kill before")
	assert.True(t, now.Equal(got.Updated))
	require.NotNil(t, got.CAs)
	assert.Equal(t, 1841, got.CAs.Points)
	require.Len(t, got.CAs.Tasks, 1)
	assert.Equal(t, caDone{Time: got.CAs.Tasks[0].Time, Task: "Duke Sucellus Master", Tier: "MASTER", Points: 5}, got.CAs.Tasks[0])
}

func TestNoPublish(t *testing.T) {
	m := newMonitor(t, newFakeWOM(t))
	_, err := m.Poll(context.Background(), monitortest.Env(now.Add(time.Hour)))
	require.NoError(t, err)
}

func TestDelves(t *testing.T) {
	f := newFakeWOM(t)
	file := filepath.Join(t.TempDir(), "live.json")
	raw, _ := json.Marshal(map[string]any{"player": "GTX 480", "womURL": f.URL, "hookSecret": "s3cret", "publish": file,
		"profileFile": `%USERPROFILE%\.runelite\profiles2\main.properties`})
	m, err := New(raw)
	require.NoError(t, err)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.NotNil(t, res.Upload)
	assert.Equal(t, []string{"delvecalculator.killCountData=", "goatindicators.totalCaught="}, res.Upload.Prefixes)
	assert.Equal(t, int64(86400), res.Upload.Every)

	upload := func(text string) error {
		return m.Act(context.Background(), env, monitor.Action{Type: monitor.UploadAction, Text: text})
	}
	assert.ErrorIs(t, upload("delvecalculator.other=1"), monitor.ErrBadAction)
	require.NoError(t, upload(`delvecalculator.killCountData={"profiles"\:{"STANDARD\:MANUAL"\:{"levelKills"\:{"6"\:34}},`+
		`"STANDARD"\:{"name"\:"STANDARD","levelKills"\:{"1"\:174,"5"\:96,"6"\:68,"7"\:33,"8"\:4},"wavesPast8"\:2},`+
		`"SEASONAL"\:{"levelKills"\:{"6"\:11},"wavesPast8"\:14}}}`+"\ngoatindicators.totalCaught=3443\n"))

	hook(t, m, env, "LOOT", `{"source":"Doom of Mokhaiotl","category":"EVENT","items":[{"id":31088,"name":"Avernic treads","quantity":1,"priceEach":30000000}]}`)
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)

	data, err := os.ReadFile(file)
	require.NoError(t, err)
	var got published
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, int64(68), got.Stats["doom_delve_6"])
	assert.Equal(t, int64(33), got.Stats["doom_delve_7"])
	assert.Equal(t, int64(4), got.Stats["doom_delve_8"])
	assert.Equal(t, int64(2), got.Stats["doom_delve_9"], "levels past 8")
	assert.Equal(t, int64(3443), got.Stats["goats_caught"])
	_, shallow := got.Stats["doom_delve_5"]
	assert.False(t, shallow, "only levels that can give Dom")
	require.Len(t, got.Drops, 1)
	assert.Equal(t, int64(107), got.Drops[0].Delves)
	assert.Equal(t, "doom_of_mokhaiotl", got.Drops[0].Metric)
}

func TestGoatsAlone(t *testing.T) {
	m := newMonitor(t, newFakeWOM(t))
	env := monitortest.Env(now)
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: monitor.UploadAction, Text: "goatindicators.totalCaught=12"}))
	assert.ErrorIs(t, m.Act(context.Background(), env, monitor.Action{Type: monitor.UploadAction, Text: "goatindicators.totalCaught=x"}), monitor.ErrBadAction)
}

func TestUnescapeProperty(t *testing.T) {
	assert.Equal(t, `{"a":1} x\y é`, unescapeProperty(`{"a"\:1}\ x\\y é`))
}
