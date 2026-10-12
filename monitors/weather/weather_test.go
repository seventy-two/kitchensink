package weather

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixture is a real response at 18:45 with rain added from 19:30, a low
// of -1.5°C tomorrow and 55 mph gusts today.
var now = time.Date(2026, 10, 10, 17, 45, 0, 0, time.UTC)

func newMonitor(t *testing.T, srv string, extra map[string]any) *Monitor {
	t.Helper()
	cfg := map[string]any{"forecastURL": srv, "place": "London", "latitude": 51.5, "longitude": -0.12}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestPoll(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/forecast.json")
	m := newMonitor(t, srv.URL, map[string]any{"frostC": 0})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)

	q, _ := url.ParseQuery(srv.Requests()[0].Query)
	assert.Equal(t, "51.5", q.Get("latitude"))
	assert.Equal(t, "auto", q.Get("timezone"))
	assert.Equal(t, "mph", q.Get("wind_speed_unit"))

	assert.Equal(t, []string{"rain:2026-10-10T19", "wind:2026-10-10", "frost:2026-10-11"}, monitortest.Keys(res.Events))
	assert.Equal(t, "Rain in London around 19:30", res.Events[0].Title)
	assert.Equal(t, "Strong winds today in London", res.Events[1].Title)
	assert.Equal(t, monitor.PriorityHigh, res.Events[1].Priority)
	assert.Equal(t, "Frost tomorrow in London", res.Events[2].Title)
	assert.Equal(t, "Low of -2°C", res.Events[2].Body)

	assert.Equal(t, "12", q.Get("forecast_hours"))
	assert.Contains(t, q.Get("hourly"), "precipitation_probability")

	require.Len(t, res.Blocks, 3)
	now := res.Blocks[0]
	assert.Equal(t, monitor.BlockStats, now.Type)
	assert.Equal(t, "13°C", now.Stats[0].Value)
	assert.Equal(t, "1.5 mm", now.Stats[3].Value)
	assert.Len(t, now.Stats[3].Series, 8)

	hourly := res.Blocks[1]
	require.Len(t, hourly.Columns, 13, "a label column, then twelve hours")
	assert.Equal(t, "Now", hourly.Columns[1].Title)
	assert.Equal(t, "20:00", hourly.Columns[3].Title)
	require.Len(t, hourly.Rows, 3)
	assert.Equal(t, "Rain", hourly.Rows[1][3].Text, "the sky, even when rain's likely")
	assert.Equal(t, "60%", hourly.Rows[2][3].Text)
	assert.Equal(t, monitor.ToneAccent, hourly.Rows[2][3].Tone)

	days := res.Blocks[2]
	require.Len(t, days.Rows, 5)
	assert.Equal(t, "Today", days.Rows[0][0].Text)
	assert.Equal(t, monitor.ToneWarn, days.Rows[0][5].Tone, "gusts over the threshold")
	assert.Equal(t, "Tomorrow", days.Rows[1][0].Text)
	assert.Equal(t, monitor.ToneAccent, days.Rows[1][3].Tone, "frost")
	assert.Equal(t, "Monday", days.Rows[2][0].Text)
}

func TestAlertsOff(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/forecast.json")
	m := newMonitor(t, srv.URL, map[string]any{"rainMM": 0, "gustMPH": 0})
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Empty(t, res.Events, "frost is off unless frostC is set")
}

func TestNoRainAlertWhileRaining(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/forecast.json")
	m := newMonitor(t, srv.URL, nil)
	f, err := m.fetch(context.Background(), srv.Client())
	require.NoError(t, err)
	f.Current.Rain = 1
	for _, e := range m.events(f) {
		assert.NotContains(t, e.Key, "rain:")
	}
}

func TestNewNeedsPlace(t *testing.T) {
	_, err := New(json.RawMessage(`{"place":"Nowhere"}`))
	assert.Error(t, err)
}

func TestSummary(t *testing.T) {
	srv := monitortest.Serve(t, "testdata/forecast.json")
	res, err := newMonitor(t, srv.URL, nil).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Equal(t, monitor.SummaryChips, res.SummaryStyle)
	require.Len(t, res.Summary, 12, "an hour to a chip")
	assert.Equal(t, monitor.Stat{Label: "Now", Value: "13°", Icon: "moon", Level: ptr(0.0)}, res.Summary[0], "now is the current temperature")
	assert.Equal(t, monitor.Stat{Label: "19:00", Value: "13°", Icon: "moon-cloud", Level: ptr(0.1)}, res.Summary[1])
	assert.Equal(t, monitor.Stat{Label: "20:00", Value: "12°", Tone: monitor.ToneAccent, Icon: "rain", Level: ptr(0.6)}, res.Summary[2],
		"likely rain is marked; the chance of it is the level")
	assert.Equal(t, monitor.ToneWarn, res.Summary[3].Tone, "strong gusts outrank rain")
	assert.Equal(t, "fog", res.Summary[10].Icon)
}

func TestIcon(t *testing.T) {
	for _, c := range []struct {
		code int
		day  bool
		want string
	}{
		{0, true, "sun"}, {1, false, "moon"}, {2, true, "sun-cloud"}, {2, false, "moon-cloud"}, {3, true, "cloud"},
		{45, true, "fog"}, {53, true, "drizzle"}, {65, false, "rain"}, {73, true, "snow"}, {86, true, "snow"},
		{81, true, "showers"}, {81, false, "rain"}, {95, true, "storm"}, {99, false, "storm"}, {10, true, ""},
	} {
		assert.Equal(t, c.want, icon(c.code, c.day), "code %d, day %v", c.code, c.day)
	}
}
