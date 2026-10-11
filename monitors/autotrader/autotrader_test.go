package autotrader

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSearchURL = "https://www.autotrader.co.uk/car-search?channel=cars&flrfc=1&make=Mazda&maxPrice=14000&model=Mazda3&postcode=SW1A%201AA&sort=relevance&transmission=Automatic&year-from=2021"

var now = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func TestParseSearch(t *testing.T) {
	s, err := parseSearch(testSearchURL)
	require.NoError(t, err)
	assert.Equal(t, "cars", s.channel)
	assert.Equal(t, "Mazda3", s.name())
	assert.Equal(t, []filter{
		{Filter: "price_search_type", Selected: []string{"total"}},
		{Filter: "make", Selected: []string{"Mazda"}},
		{Filter: "max_price", Selected: []string{"14000"}},
		{Filter: "model", Selected: []string{"Mazda3"}},
		{Filter: "postcode", Selected: []string{"SW1A 1AA"}},
		{Filter: "transmission", Selected: []string{"Automatic"}},
		{Filter: "min_year_manufactured", Selected: []string{"2021"}},
	}, s.filters)

	_, err = parseSearch("https://www.autotrader.co.uk/car-search?postcode=SW1A")
	assert.Error(t, err, "a search needs a make")
}

func listingJSON(id, typ string) string {
	return `{"type":"` + typ + `","advertId":"` + id + `","title":"Mazda Mazda3","subTitle":"2.0 Sport Lux Auto","price":"£13,900","vehicleLocation":"Aldershot (82 miles)","sellerType":"PRIVATE","attentionGrabber":"Full service history","images":["https://m.atcdn.co.uk/a/media/{resize}/abc.jpg"],"badges":[{"type":"MILEAGE","displayText":"36,500 miles"},{"type":"REGISTERED_YEAR","displayText":"2022 (22 reg)"},{"type":"WRITE_OFF_CATEGORY","displayText":"Cat N"},{"type":"PI_GOOD","displayText":"Good price"}]}`
}

func results(listings ...string) []byte {
	return []byte(`[{"data":{"searchResults":{"listings":[` + strings.Join(listings, ",") + `]}}}]`)
}

func newMonitor(t *testing.T, gw string) *Monitor {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"searchURL": testSearchURL, "gatewayURL": gw})
	m, err := New(raw)
	require.NoError(t, err)
	return m
}

func TestFirstPollIsQuiet(t *testing.T) {
	srv := monitortest.Serve(t, "/dev/null")
	srv.Reply(http.StatusOK, results(listingJSON("1", "NATURAL_LISTING"), listingJSON("ad", "PROMOTED_LISTING")))
	m := newMonitor(t, srv.URL)
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, []string{"advert:1"}, monitortest.Keys(res.Events), "promoted adverts are ignored")
	assert.True(t, res.Events[0].Quiet, "already listed on the first check")

	srv.Reply(http.StatusOK, results(listingJSON("2", "NATURAL_LISTING"), listingJSON("1", "NATURAL_LISTING")))
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Events, 2, "the engine drops the repeat of advert 1")
	e := res.Events[0]
	assert.False(t, e.Quiet)
	assert.Equal(t, "advert:2", e.Key)
	assert.Equal(t, "New Mazda3 · £13,900", e.Title)
	assert.Equal(t, "36,500 miles · 2022 (22 reg) · Aldershot (82 miles) · Private · Cat N write-off", e.Body)
	assert.Equal(t, "https://www.autotrader.co.uk/car-details/2", e.URL)
	assert.Equal(t, "https://m.atcdn.co.uk/a/media/w800h600/abc.jpg", e.Image)
	assert.Equal(t, monitor.PriorityHigh, e.Priority)

	var body []map[string]any
	require.NoError(t, json.Unmarshal(srv.Requests()[0].Body, &body))
	vars := body[0]["variables"].(map[string]any)
	assert.Equal(t, "most_recent", vars["sortBy"])
	assert.Len(t, vars["filters"], 7)
	assert.Equal(t, userAgent, srv.Requests()[0].Header.Get("User-Agent"))
}

func TestList(t *testing.T) {
	srv := monitortest.Serve(t, "/dev/null")
	srv.Reply(http.StatusOK, results(listingJSON("1", "NATURAL_LISTING")))
	res, err := newMonitor(t, srv.URL).Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	require.Len(t, res.Blocks, 1)
	b := res.Blocks[0]
	assert.Equal(t, "1 listed", b.Title)
	require.Len(t, b.Items, 1)
	assert.Equal(t, "Mazda Mazda3 · £13,900", b.Items[0].Title)
	assert.Equal(t, "2.0 Sport Lux Auto", b.Items[0].Detail)
	assert.Equal(t, monitor.ToneWarn, b.Items[0].Tone, "write-offs are flagged")
}

func TestPollErrors(t *testing.T) {
	for name, reply := range map[string]struct {
		status int
		body   string
	}{
		"blocked":       {http.StatusForbidden, "challenge"},
		"graphql error": {http.StatusOK, `[{"errors":[{"message":"Some input filters were invalid"}]}]`},
		"not json":      {http.StatusOK, `<html>Just a moment...</html>`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := monitortest.Serve(t, "/dev/null")
			srv.Reply(reply.status, []byte(reply.body))
			env := monitortest.Env(now)
			_, err := newMonitor(t, srv.URL).Poll(context.Background(), env)
			assert.Error(t, err)
			var baselined bool
			ok, _ := env.State.Get(baselineKey, &baselined)
			assert.False(t, ok, "a failed check doesn't count as the baseline")
		})
	}
}

func TestNewSearchID(t *testing.T) {
	id := newSearchID()
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, id)
	assert.NotEqual(t, id, newSearchID())
}

func TestSummary(t *testing.T) {
	car := func(price, year string) listing {
		l := listing{Price: price}
		l.Badges = append(l.Badges, struct {
			Type        string `json:"type"`
			DisplayText string `json:"displayText"`
		}{"REGISTERED_YEAR", year})
		return l
	}
	got := summary([]listing{car("£13,871", "2024"), car("£12,000", "2022"), car("£12,700", "2023")})
	assert.Equal(t, []monitor.Stat{
		{Label: "Listed", Value: "3"},
		{Label: "Newest", Value: "£13,871", Detail: "2024"},
		{Label: "Cheapest", Value: "£12,000", Detail: "2022"},
	}, got)

	got = summary([]listing{car("£9,000", "2020")})
	assert.Len(t, got, 2, "no cheapest when it's the newest")
	assert.Equal(t, []monitor.Stat{{Label: "Listed", Value: "0"}}, summary(nil))
}

func TestPrice(t *testing.T) {
	p, ok := price("£13,871")
	assert.True(t, ok)
	assert.Equal(t, 13871, p)
	p, _ = price("£9,999.50")
	assert.Equal(t, 9999, p)
	_, ok = price("POA")
	assert.False(t, ok)
}

func TestDescribe(t *testing.T) {
	for raw, want := range map[string]string{
		testSearchURL: "Mazda3 · Automatic · up to £14,000 · 2021 on",
		"https://www.autotrader.co.uk/car-search?make=Ford&model=Focus&price-from=5000&price-to=12500&year-from=2018&year-to=2020": "Ford Focus · £5,000–£12,500 · 2018–2020",
		"https://www.autotrader.co.uk/car-search?make=Honda&postcode=SW1A%201AA":                                                   "Honda",
	} {
		s, err := parseSearch(raw)
		require.NoError(t, err)
		assert.Equal(t, want, s.describe(), raw)
		assert.NotContains(t, s.describe(), "SW1A", "no postcode")
	}
}
