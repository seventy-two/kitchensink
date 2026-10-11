package calendar

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var london, _ = time.LoadLocation("Europe/London")

func TestExpand(t *testing.T) {
	// Thursday 1 October 2026, 09:00 BST.
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, london)
	far := time.Date(2030, 1, 1, 0, 0, 0, 0, london)
	tests := []struct {
		rule string
		to   time.Time
		want []string
	}{
		{"FREQ=DAILY;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Fri 02 Oct 09:00", "Sat 03 Oct 09:00"}},
		{"FREQ=DAILY;INTERVAL=2;UNTIL=20261005", far, []string{"Thu 01 Oct 09:00", "Sat 03 Oct 09:00", "Mon 05 Oct 09:00"}},
		{"FREQ=DAILY;BYDAY=MO,TU,WE,TH,FR;COUNT=4", far, []string{"Thu 01 Oct 09:00", "Fri 02 Oct 09:00", "Mon 05 Oct 09:00", "Tue 06 Oct 09:00"}},
		// Stays at 09:00 local after the clocks go back on 25 October.
		{"FREQ=WEEKLY;BYDAY=TH;COUNT=5", far, []string{"Thu 01 Oct 09:00", "Thu 08 Oct 09:00", "Thu 15 Oct 09:00", "Thu 22 Oct 09:00", "Thu 29 Oct 09:00"}},
		{"FREQ=WEEKLY;BYDAY=TU,TH;COUNT=4", far, []string{"Thu 01 Oct 09:00", "Tue 06 Oct 09:00", "Thu 08 Oct 09:00", "Tue 13 Oct 09:00"}},
		{"FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,TH;WKST=MO;COUNT=4", far, []string{"Thu 01 Oct 09:00", "Mon 12 Oct 09:00", "Thu 15 Oct 09:00", "Mon 26 Oct 09:00"}},
		{"FREQ=MONTHLY;BYDAY=2TU;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Tue 13 Oct 09:00", "Tue 10 Nov 09:00"}},
		{"FREQ=MONTHLY;BYDAY=-1FR;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Fri 30 Oct 09:00", "Fri 27 Nov 09:00"}},
		// The last weekday of the month, as Outlook writes it.
		{"FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Fri 30 Oct 09:00", "Mon 30 Nov 09:00"}},
		{"FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Sat 31 Oct 09:00", "Mon 30 Nov 09:00"}},
		{"FREQ=MONTHLY;COUNT=3", far, []string{"Thu 01 Oct 09:00", "Sun 01 Nov 09:00", "Tue 01 Dec 09:00"}},
		{"FREQ=YEARLY;COUNT=2", far, []string{"Thu 01 Oct 09:00", "Fri 01 Oct 09:00"}},
		{"FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU;COUNT=2", far, []string{"Thu 01 Oct 09:00", "Sun 28 Mar 09:00"}},
		{"FREQ=WEEKLY", time.Date(2026, 10, 15, 9, 0, 0, 0, london), []string{"Thu 01 Oct 09:00", "Thu 08 Oct 09:00", "Thu 15 Oct 09:00"}},
	}
	for _, tt := range tests {
		t.Run(tt.rule, func(t *testing.T) {
			r, err := parseRRule(tt.rule, london)
			require.NoError(t, err)
			var got []string
			for _, o := range r.expand(start, start, tt.to) {
				got = append(got, o.In(london).Format("Mon 02 Jan 15:04"))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExpandMonthEnd(t *testing.T) {
	start := time.Date(2026, 1, 31, 9, 0, 0, 0, london)
	r, err := parseRRule("FREQ=MONTHLY;COUNT=3", london)
	require.NoError(t, err)
	got := r.expand(start, start, start.AddDate(2, 0, 0))
	require.Len(t, got, 3)
	assert.Equal(t, time.March, got[1].Month(), "February has no 31st")
	assert.Equal(t, time.May, got[2].Month())
}

func TestExpandFrom(t *testing.T) {
	// Only occurrences from `from` come back, but COUNT still counts the
	// ones before it.
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, london)
	r, err := parseRRule("FREQ=DAILY;COUNT=5", london)
	require.NoError(t, err)
	got := r.expand(start, time.Date(2026, 10, 4, 0, 0, 0, 0, london), start.AddDate(1, 0, 0))
	require.Len(t, got, 2)
	assert.Equal(t, 4, got[0].Day())
	assert.Equal(t, 5, got[1].Day())
}

func TestParseRRuleErrors(t *testing.T) {
	for _, s := range []string{"FREQ=HOURLY", "FREQ=DAILY;INTERVAL=0", "FREQ=WEEKLY;BYDAY=XX", "COUNT=2"} {
		_, err := parseRRule(s, london)
		assert.Error(t, err, s)
	}
}
