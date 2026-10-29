package lights

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

var now = time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)

// statesJSON is shaped like Home Assistant 2026.10's /api/states: a colour
// bulb on at full (brightness as a float), one off (nulls), an on/off-only
// light, one unavailable and something that isn't a light.
const statesJSON = `[
 {"entity_id": "light.desk", "state": "on", "attributes": {
   "min_color_temp_kelvin": 2127, "max_color_temp_kelvin": 6535,
   "supported_color_modes": ["color_temp", "hs"], "color_mode": "hs",
   "brightness": 255.0, "hs_color": [38.0, 3.0], "rgb_color": [255, 251, 247], "friendly_name": "Desk lamp", "supported_features": 0},
  "last_changed": "2026-10-26T13:47:21.121023+00:00", "context": {"id": "x", "parent_id": null, "user_id": null}},
 {"entity_id": "light.bedside", "state": "off", "attributes": {
   "supported_color_modes": ["color_temp", "hs"], "color_mode": null, "brightness": null,
   "friendly_name": "Bedside", "supported_features": 0}},
 {"entity_id": "light.hall", "state": "on", "attributes": {
   "supported_color_modes": ["onoff"], "color_mode": "onoff", "friendly_name": "hall"}},
 {"entity_id": "light.porch", "state": "unavailable", "attributes": {
   "supported_color_modes": ["brightness"], "friendly_name": "Porch", "restored": true}},
 {"entity_id": "sun.sun", "state": "below_horizon", "attributes": {"friendly_name": "Sun"}}
]`

// fakeHA is a Home Assistant that serves states and records service calls.
type fakeHA struct {
	*httptest.Server
	mu     sync.Mutex
	states string
	areas  map[string][]string // area → light IDs
	status int                 // if set, every answer
	calls  []string            // "service {json data}"
}

const token = "test-token"

func newFakeHA(t *testing.T) *fakeHA {
	f := &fakeHA{states: statesJSON}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeHA) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+token {
		http.Error(w, "401: Unauthorized", http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/states":
		io.WriteString(w, f.states)
	case r.Method == http.MethodPost && r.URL.Path == "/api/template":
		var req struct{ Template string }
		json.Unmarshal(body, &req)
		ids := []string{}
		for area, lights := range f.areas {
			if strings.Contains(req.Template, `"`+area+`"`) {
				ids = append(ids, lights...)
			}
		}
		json.NewEncoder(w).Encode(ids)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/services/light/"):
		f.calls = append(f.calls, strings.TrimPrefix(r.URL.Path, "/api/services/light/")+" "+string(body))
		io.WriteString(w, "[]")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHA) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func newMonitor(t *testing.T, url string, extra string) *Monitor {
	t.Helper()
	m, err := New(json.RawMessage(fmt.Sprintf(`{"url": %q, "token": %q%s}`, url+"/", token, extra)))
	require.NoError(t, err)
	return m
}

// rows is the checklist as "[x] title" lines.
func rows(t *testing.T, res monitor.Result) []string {
	t.Helper()
	require.Len(t, res.Blocks, 1)
	b := res.Blocks[0]
	require.Equal(t, monitor.BlockChecklist, b.Type)
	var out []string
	for _, it := range b.Items {
		mark := "[ ] "
		if it.Done {
			mark = "[x] "
		}
		title := it.Title
		if it.Subtitle != "" {
			title += " · " + strings.ToLower(it.Subtitle)
		}
		out = append(out, mark+title)
	}
	return out
}

func TestPanel(t *testing.T) {
	ha := newFakeHA(t)
	m := newMonitor(t, ha.URL, "")
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	// Every light by name, case aside; ticked means off.
	assert.Equal(t, []string{
		"[x] Bedside",
		"[ ] Desk lamp · 100%",
		"[ ] hall · on",
		"[x] Porch · unavailable",
	}, rows(t, res))
	b := res.Blocks[0]
	assert.True(t, b.Switch)
	// Short, the same lights as buttons.
	require.NotNil(t, res.SummaryBlock)
	assert.True(t, res.SummaryBlock.Buttons)
	assert.Equal(t, b.Items, res.SummaryBlock.Items)
	assert.True(t, b.NoDelete)
	assert.True(t, b.Movable)
	assert.Equal(t, "Brighter", b.UpHint)
	assert.Equal(t, "Dimmer", b.DownHint)
	assert.Equal(t, "light.desk", b.Items[1].ID)

	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	assert.Equal(t, []monitor.Stat{
		{Label: "Lights", Value: "2 of 4 on", Tone: monitor.ToneAccent, Lead: true},
		{Label: "Desk lamp", Value: "100%", Tone: monitor.ToneAccent},
		{Label: "hall", Value: "on", Tone: monitor.ToneAccent},
		{Label: "Porch", Value: "Unavailable", Tone: monitor.ToneWarn},
	}, res.Summary)
	assert.Empty(t, res.Events, "light changes make no events")
	assert.Zero(t, res.Next)
}

func TestIncludeAndAreas(t *testing.T) {
	ha := newFakeHA(t)
	ha.areas = map[string][]string{"Bedroom": {"light.bedside", "light.desk"}}

	// Lights in their own order, a missing one shown as unavailable.
	m := newMonitor(t, ha.URL, `, "lights": ["light.hall", "light.gone", "light.desk"]`)
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"[ ] hall · on",
		"[x] light.gone · unavailable",
		"[ ] Desk lamp · 100%",
	}, rows(t, res))

	// Areas' lights follow, by name, without repeating any.
	m = newMonitor(t, ha.URL, `, "lights": ["light.desk"], "areas": ["Bedroom"]`)
	res, err = m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, []string{"[ ] Desk lamp · 100%", "[x] Bedside"}, rows(t, res))

	// None at all is a note.
	m = newMonitor(t, ha.URL, `, "areas": ["Nowhere", "Bedroom"]`)
	ha.areas = map[string][]string{"Bedroom": {"light.bedside"}}
	res, err = m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, []string{"[x] Bedside"}, rows(t, res))
	assert.Equal(t, []monitor.Stat{{Label: "Lights", Value: "All off", Tone: monitor.ToneMuted, Lead: true}}, res.Summary)

	ha.areas = nil
	res, err = m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	require.Len(t, res.Blocks, 1)
	assert.Equal(t, monitor.BlockText, res.Blocks[0].Type)
}

func TestActions(t *testing.T) {
	ha := newFakeHA(t)
	m := newMonitor(t, ha.URL, "")
	env := monitortest.Env(now)
	ctx := context.Background()
	act := func(typ, id, text string) error {
		return m.Act(ctx, env, monitor.Action{Type: typ, ID: id, Text: text})
	}

	require.NoError(t, act("toggle", "light.bedside", ""))
	require.NoError(t, act("toggle", "light.desk", ""))
	assert.Equal(t, []string{`turn_on {"entity_id":["light.bedside"]}`, `turn_off {"entity_id":["light.desk"]}`}, ha.took())

	// All off while any is on; all on (the available ones) once none are.
	require.NoError(t, act("toggle", allID, ""))
	assert.Equal(t, []string{`turn_off {"entity_id":["light.desk","light.hall"]}`}, ha.took())
	ha.states = strings.NewReplacer(`"state": "on"`, `"state": "off"`).Replace(statesJSON)
	require.NoError(t, act("toggle", allID, ""))
	assert.Equal(t, []string{`turn_on {"entity_id":["light.bedside","light.desk","light.hall"]}`}, ha.took())
	ha.states = statesJSON

	// Brighter and dimmer step between the levels; past the ends, and
	// on lights that are off or can't dim, nothing happens.
	require.NoError(t, act("move", "light.desk", "down"))
	assert.Equal(t, []string{`turn_on {"brightness_pct":75,"entity_id":["light.desk"]}`}, ha.took())
	require.NoError(t, act("move", "light.desk", "up"))
	require.NoError(t, act("move", "light.bedside", "up"))
	require.NoError(t, act("move", "light.hall", "up"))
	require.NoError(t, act("move", allID, "up"))
	assert.Empty(t, ha.took())
	ha.states = strings.Replace(statesJSON, `"brightness": 255.0`, `"brightness": 30.0`, 1) // 12%
	require.NoError(t, act("move", allID, "down"))
	assert.Equal(t, []string{`turn_on {"brightness_pct":10,"entity_id":["light.desk"]}`}, ha.took())
	require.NoError(t, act("move", allID, "up"))
	assert.Equal(t, []string{`turn_on {"brightness_pct":25,"entity_id":["light.desk"]}`}, ha.took())
	ha.states = statesJSON

	require.NoError(t, act("brightness", "light.bedside", "40%"))
	require.NoError(t, act("brightness", "light.desk", "0"))
	require.NoError(t, act("brightness", "light.hall", "50")) // can't dim
	assert.Equal(t, []string{
		`turn_on {"brightness_pct":40,"entity_id":["light.bedside"]}`,
		`turn_off {"entity_id":["light.desk"]}`,
	}, ha.took())

	// Colours: any colour as hue and saturation, whites as kelvin, one
	// call per light; lights that can't take it are left alone.
	require.NoError(t, act("colour", "light.desk", "#ff8000"))
	require.NoError(t, act("colour", "light.bedside", "2700k"))
	require.NoError(t, act("colour", "light.hall", "#00ff00"))
	assert.Equal(t, []string{
		`turn_on {"entity_id":"light.desk","hs_color":[30.1,100]}`,
	}, ha.took())
	require.NoError(t, act("colour", allID, "#0000ff"))
	assert.Equal(t, []string{
		`turn_on {"entity_id":"light.bedside","hs_color":[240,100]}`,
		`turn_on {"entity_id":"light.desk","hs_color":[240,100]}`,
	}, ha.took())
	require.NoError(t, act("colour", allID, "2700K"))
	assert.Equal(t, []string{`turn_on {"color_temp_kelvin":2700,"entity_id":"light.desk"}`}, ha.took())

	for _, bad := range []monitor.Action{
		{Type: "colour", ID: "light.desk", Text: "red"},
		{Type: "colour", ID: "light.desk", Text: "#ff00"},
		{Type: "colour", ID: "light.desk", Text: "99999K"},
		{Type: "toggle", ID: "light.nope"},
		{Type: "toggle", ID: "light.porch"}, // unavailable
		{Type: "move", ID: "light.desk", Text: "sideways"},
		{Type: "brightness", ID: "light.desk", Text: "150"},
		{Type: "delete", ID: "light.desk"},
	} {
		assert.ErrorIs(t, m.Act(ctx, env, bad), monitor.ErrBadAction, "%+v", bad)
	}
	assert.Empty(t, ha.took())

	// After an action, the next poll comes soon, for lights that report
	// late; then the interval again.
	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Equal(t, recheckAfter, res.Next)
	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Zero(t, res.Next)
}

func TestUnreachable(t *testing.T) {
	ha := newFakeHA(t)
	m, err := New(json.RawMessage(fmt.Sprintf(`{"url": %q, "token": %q, "offlineAfter": "30m"}`, ha.URL, token)))
	require.NoError(t, err)
	clock := now
	env := monitortest.Env(now)
	env.Now = func() time.Time { return clock }
	ctx := context.Background()

	ha.status = http.StatusBadGateway // the tunnel's down
	res, err := m.Poll(ctx, env)
	require.ErrorIs(t, err, errUnreachable)
	assert.Empty(t, res.Events)
	assert.Error(t, m.Act(ctx, env, monitor.Action{Type: "toggle", ID: "light.desk"}))

	// HA itself gone: the error doesn't repeat the address.
	ha.Close()
	clock = now.Add(29 * time.Minute)
	res, err = m.Poll(ctx, env)
	require.ErrorIs(t, err, errUnreachable)
	assert.NotContains(t, err.Error(), "/api/states")
	assert.Empty(t, res.Events)

	// One quiet event once it's been down long enough, and no more.
	clock = now.Add(31 * time.Minute)
	res, _ = m.Poll(ctx, env)
	require.Len(t, res.Events, 1)
	ev := res.Events[0]
	assert.Equal(t, "Can't reach Home Assistant", ev.Title)
	assert.True(t, ev.Quiet)
	assert.Equal(t, monitor.PriorityLow, ev.Priority)
	clock = now.Add(2 * time.Hour)
	res, _ = m.Poll(ctx, env)
	assert.Empty(t, res.Events)

	// Back: the event is updated, and goes from Recent soon.
	ha2 := newFakeHA(t)
	m.cfg.URL = ha2.URL
	clock = now.Add(2*time.Hour + 5*time.Minute)
	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, ev.Key, res.Events[0].Key)
	assert.True(t, res.Events[0].Update)
	assert.Equal(t, "Home Assistant was unreachable for 2h 5m", res.Events[0].Title)
	assert.Equal(t, clock.Add(time.Hour), res.Events[0].Until)
	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)

	// A short blip makes no event at all.
	ha2.status = http.StatusServiceUnavailable
	_, err = m.Poll(ctx, env)
	require.Error(t, err)
	ha2.status = 0
	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Empty(t, res.Events)
}

func TestBadToken(t *testing.T) {
	ha := newFakeHA(t)
	m, err := New(json.RawMessage(fmt.Sprintf(`{"url": %q, "token": "wrong"}`, ha.URL)))
	require.NoError(t, err)
	_, err = m.Poll(context.Background(), monitortest.Env(now))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refused the token")
	assert.NotErrorIs(t, err, errUnreachable)
}

func TestConfig(t *testing.T) {
	for _, bad := range []string{
		`{"token": "t"}`,
		`{"url": "homeassistant.local", "token": "t"}`,
		`{"url": "http://ha.local"}`,
		`{"url": "http://ha.local", "token": "t", "lights": ["switch.fan"]}`,
		`{"url": "http://ha.local", "token": "t", "levels": [0, 50]}`,
		`{"url": "http://ha.local", "token": "t", "offlineAfter": "-1m"}`,
		`{"url": "http://ha.local", "token": "t", "colour": true}`,
	} {
		_, err := New(json.RawMessage(bad))
		assert.Error(t, err, bad)
	}
	m, err := New(json.RawMessage(`{"url": "http://ha.local/", "token": "t", "levels": [50, 5, 50]}`))
	require.NoError(t, err)
	assert.Equal(t, "http://ha.local", m.cfg.URL)
	assert.Equal(t, []int{5, 50}, m.cfg.Levels)
	assert.Equal(t, monitor.Duration(30*time.Minute), m.cfg.OfflineAfter)
}

func TestStep(t *testing.T) {
	levels := []int{10, 25, 50, 75, 100}
	for _, c := range []struct {
		pct  int
		up   bool
		want int
		ok   bool
	}{
		{1, true, 10, true}, {10, true, 25, true}, {60, true, 75, true}, {100, true, 0, false},
		{100, false, 75, true}, {60, false, 50, true}, {11, false, 10, true}, {10, false, 0, false},
	} {
		got, ok := step(c.pct, levels, c.up)
		assert.Equal(t, c.want, got, "%+v", c)
		assert.Equal(t, c.ok, ok, "%+v", c)
	}
	assert.Equal(t, "40m", span(40*time.Minute))
	assert.Equal(t, "2h", span(2*time.Hour))
}

func TestControls(t *testing.T) {
	ha := newFakeHA(t)
	m := newMonitor(t, ha.URL, "")
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	items := res.Blocks[0].Items
	require.Len(t, items, 4)
	bedside, desk, hall, porch := items[0], items[1], items[2], items[3]

	// The desk lamp: at full, its colour now, whites in its range and hues.
	require.NotNil(t, desk.Slider)
	assert.Equal(t, monitor.Slider{Value: 100, Action: "brightness", Hint: "Brightness"}, *desk.Slider)
	require.NotNil(t, desk.Colour)
	assert.Equal(t, "#fffbf7", desk.Colour.Value)
	assert.True(t, desk.Colour.Wheel)
	var labels []string
	for _, p := range desk.Colour.Presets {
		labels = append(labels, p.Label)
	}
	assert.Equal(t, []string{"Candle", "Warm white", "Soft white", "Neutral white", "Daylight"}, labels[:5])
	assert.Equal(t, monitor.Swatch{Label: "Warm white", Value: "2700K", Show: "#ffa757"}, desk.Colour.Presets[1])
	assert.Contains(t, desk.Colour.Presets, monitor.Swatch{Label: "Blue", Value: "#0033ff", Show: "#0033ff"})

	// Off: the slider at 0 and no colour now, but still the hues (no
	// whites, as HA leaves out the range here).
	assert.Equal(t, 0, bedside.Slider.Value)
	assert.Empty(t, bedside.Colour.Value)
	assert.Equal(t, "Red", bedside.Colour.Presets[0].Label)

	// On/off only: neither. Unavailable: neither.
	assert.Nil(t, hall.Slider)
	assert.Nil(t, hall.Colour)
	assert.Nil(t, porch.Slider)
	assert.Nil(t, porch.Colour)
}

func TestHexHS(t *testing.T) {
	for in, want := range map[string][2]float64{
		"#ff0000": {0, 100}, "#00ff00": {120, 100}, "#0000ff": {240, 100},
		"#ffffff": {0, 0}, "#000000": {0, 0}, "#ff8000": {30.1, 100}, "#FF4D8C": {338.8, 69.8},
	} {
		h, s, ok := hexHS(in)
		require.True(t, ok, in)
		assert.Equal(t, want, [2]float64{h, s}, in)
	}
	for _, bad := range []string{"", "ff0000", "#ff00", "#gg0000"} {
		_, _, ok := hexHS(bad)
		assert.False(t, ok, bad)
	}
}

func TestKelvinRGB(t *testing.T) {
	assert.Equal(t, "#ff9227", kelvinRGB(2200))
	assert.Equal(t, "#fffefa", kelvinRGB(6500))
}
