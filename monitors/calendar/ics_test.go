package calendar

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseICS(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Europe/London\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\n" +
		"SUMMARY:A long\r\n  title\\, folded\r\n" +
		"DTSTART;TZID=\"GMT Standard Time\":20261012T090000\r\n" +
		"BEGIN:VALARM\r\nSUMMARY:not this\r\nEND:VALARM\r\n" +
		"EXDATE:20261013T080000Z,20261014T080000Z\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	events, err := parseICS(strings.NewReader(ics))
	require.NoError(t, err)
	require.Len(t, events, 1)
	e := events[0]
	assert.Equal(t, "A long title, folded", e.text("SUMMARY"), "folded, escaped, and the alarm's summary ignored")

	p, _ := e.get("DTSTART")
	assert.Equal(t, "GMT Standard Time", p.Params["TZID"])
	start, allDay, err := parseTime(p, time.UTC)
	require.NoError(t, err)
	assert.False(t, allDay)
	assert.Equal(t, "2026-10-12T09:00:00+01:00", start.Format(time.RFC3339), "Windows zone name mapped to London")

	assert.Len(t, parseTimes(e["EXDATE"][0], time.UTC), 2)
}

func TestParseLine(t *testing.T) {
	p, ok := parseLine(`ATTENDEE;CN="Smith; Jo";ROLE=REQ:mailto:jo@example.com`)
	require.True(t, ok)
	assert.Equal(t, "ATTENDEE", p.Name)
	assert.Equal(t, "Smith; Jo", p.Params["CN"])
	assert.Equal(t, "mailto:jo@example.com", p.Value)

	_, ok = parseLine("no colon here")
	assert.False(t, ok)
}

func TestLookupZone(t *testing.T) {
	assert.Equal(t, "Europe/Paris", lookupZone("Europe/Paris", time.UTC).String())
	assert.Equal(t, "America/New_York", lookupZone("Eastern Standard Time", time.UTC).String())
	assert.Equal(t, "Europe/London", lookupZone("/mozilla.org/20050126_1/Europe/London", time.UTC).String())
	assert.Equal(t, london, lookupZone("Customized Time Zone", london))
}

func TestParseDuration(t *testing.T) {
	for s, want := range map[string]time.Duration{
		"PT1H30M": 90 * time.Minute,
		"P1D":     24 * time.Hour,
		"P1W":     7 * 24 * time.Hour,
		"-PT15M":  -15 * time.Minute,
		"P1DT2H":  26 * time.Hour,
	} {
		got, err := parseDuration(s)
		require.NoError(t, err, s)
		assert.Equal(t, want, got, s)
	}
	for _, s := range []string{"1H", "PT1X", "PT1"} {
		_, err := parseDuration(s)
		assert.Error(t, err, s)
	}
}
