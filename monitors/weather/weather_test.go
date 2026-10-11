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

	require.Len(t, res.Blocks, 2)
	now := res.Blocks[0]
	assert.Equal(t, monitor.BlockStats, now.Type)
	assert.Equal(t, "13°C", now.Stats[0].Value)
	assert.Equal(t, "1.5 mm", now.Stats[3].Value)
	assert.Len(t, now.Stats[3].Series, 8)

	days := res.Blocks[1]
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
	require.Len(t, res.Summary, 3)
	assert.Equal(t, monitor.Stat{Label: "Now", Value: "13°C", Detail: "Mostly clear"}, res.Summary[0])
	assert.Equal(t, "1.5 mm", res.Summary[1].Value)
	assert.Equal(t, monitor.Stat{Label: "Today", Value: "15° / 11°", Detail: "0% rain · gusts 55", Tone: monitor.ToneWarn}, res.Summary[2])
}
