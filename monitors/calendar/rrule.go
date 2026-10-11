package calendar

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rrule is a parsed RRULE. It supports what calendar apps write for
// meetings and reminders: DAILY, WEEKLY, MONTHLY and YEARLY with INTERVAL,
// COUNT, UNTIL, BYDAY (with ordinals such as 2TU or -1FR), BYMONTHDAY,
// BYMONTH, BYSETPOS and WKST.
type rrule struct {
	freq       string
	interval   int
	count      int
	until      time.Time // zero for none
	byDay      []weekdayNum
	byMonthDay []int
	byMonth    []time.Month
	bySetPos   []int
	wkst       time.Weekday
}

// weekdayNum is a BYDAY entry: a weekday and, for MONTHLY and YEARLY, which
// one in the month or year (1 is the first, -1 the last, 0 every one).
type weekdayNum struct {
	n  int
	wd time.Weekday
}

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// parseRRule parses an RRULE value. loc is the zone of a floating or
// date-only UNTIL.
func parseRRule(s string, loc *time.Location) (*rrule, error) {
	r := &rrule{interval: 1, wkst: time.Monday}
	for _, part := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		var err error
		switch strings.ToUpper(k) {
		case "FREQ":
			r.freq = strings.ToUpper(v)
		case "INTERVAL":
			r.interval, err = strconv.Atoi(v)
			if err == nil && r.interval < 1 {
				err = fmt.Errorf("bad INTERVAL %q", v)
			}
		case "COUNT":
			r.count, err = strconv.Atoi(v)
		case "UNTIL":
			var allDay bool
			r.until, allDay, err = parseTime(prop{Value: v}, loc)
			if allDay {
				// A date-only UNTIL includes that whole day.
				r.until = r.until.AddDate(0, 0, 1).Add(-time.Nanosecond)
			}
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				d = strings.ToUpper(strings.TrimSpace(d))
				if len(d) < 2 {
					return nil, fmt.Errorf("bad BYDAY %q", v)
				}
				wd, ok := weekdays[d[len(d)-2:]]
				if !ok {
					return nil, fmt.Errorf("bad BYDAY %q", v)
				}
				n := 0
				if num := d[:len(d)-2]; num != "" {
					if n, err = strconv.Atoi(num); err != nil {
						return nil, fmt.Errorf("bad BYDAY %q", v)
					}
				}
				r.byDay = append(r.byDay, weekdayNum{n, wd})
			}
		case "BYMONTHDAY":
			r.byMonthDay, err = ints(v)
		case "BYMONTH":
			var ms []int
			ms, err = ints(v)
			for _, m := range ms {
				r.byMonth = append(r.byMonth, time.Month(m))
			}
		case "BYSETPOS":
			r.bySetPos, err = ints(v)
		case "WKST":
			if wd, ok := weekdays[strings.ToUpper(v)]; ok {
				r.wkst = wd
			}
		}
		if err != nil {
			return nil, fmt.Errorf("RRULE %s: %w", k, err)
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return nil, fmt.Errorf("unsupported RRULE FREQ %q", r.freq)
	}
	return r, nil
}

func ints(s string) ([]int, error) {
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// maxPeriods bounds expansion: a daily rule started decades ago still
// finishes quickly, and a broken rule can't loop forever.
const maxPeriods = 50000

// expand lists the rule's occurrences from start, keeping those at or after
// from and stopping after to. Each occurrence keeps start's wall-clock time
// in start's zone, so a 9am meeting stays at 9am across clock changes.
// start itself is always the first occurrence, as RFC 5545 says.
func (r *rrule) expand(start, from, to time.Time) []time.Time {
	var out []time.Time
	n := 0
	emit := func(t time.Time) bool {
		if (!r.until.IsZero() && t.After(r.until)) || t.After(to) || (r.count > 0 && n >= r.count) {
			return false
		}
		n++
		if !t.Before(from) {
			out = append(out, t)
		}
		return true
	}
	if !emit(start) {
		return out
	}
	for k := 0; k < maxPeriods; k++ {
		for _, t := range r.period(start, k) {
			if !t.After(start) {
				continue
			}
			if !emit(t) {
				return out
			}
		}
	}
	return out
}

// period returns the candidate occurrences in the k'th period (day, week,
// month or year) after start's, in order.
func (r *rrule) period(start time.Time, k int) []time.Time {
	y, mo, d := start.Date()
	h, mi, s := start.Clock()
	loc := start.Location()
	at := func(y int, mo time.Month, d int) time.Time { return time.Date(y, mo, d, h, mi, s, 0, loc) }
	step := k * r.interval

	var days []time.Time
	switch r.freq {
	case "DAILY":
		t := at(y, mo, d+step)
		if r.matchMonth(t) && r.matchMonthDay(t) && r.matchWeekday(t) {
			days = append(days, t)
		}
	case "WEEKLY":
		back := (int(start.Weekday()) - int(r.wkst) + 7) % 7
		first := d - back + 7*step
		wds := r.byDay
		if len(wds) == 0 {
			wds = []weekdayNum{{0, start.Weekday()}}
		}
		for i := 0; i < 7; i++ {
			t := at(y, mo, first+i)
			for _, w := range wds {
				if t.Weekday() == w.wd && r.matchMonth(t) {
					days = append(days, t)
				}
			}
		}
	case "MONTHLY":
		first := at(y, mo+time.Month(step), 1)
		if r.matchMonth(first) {
			days = r.monthDays(first, d)
		}
	case "YEARLY":
		yr := y + step
		switch {
		case len(r.byMonth) > 0:
			for _, m := range r.byMonth {
				days = append(days, r.monthDays(at(yr, m, 1), d)...)
			}
		case len(r.byDay) > 0 && len(r.byMonthDay) == 0:
			days = r.yearDays(at(yr, time.January, 1))
		case len(r.byMonthDay) > 0:
			for m := time.January; m <= time.December; m++ {
				days = append(days, r.monthDays(at(yr, m, 1), d)...)
			}
		default:
			days = r.monthDays(at(yr, mo, 1), d)
		}
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	return r.setPos(dedupe(days))
}

// monthDays lists the matching days in first's month. Without BYMONTHDAY or
// BYDAY it's the day of the month the series started on, when the month has
// one.
func (r *rrule) monthDays(first time.Time, startDay int) []time.Time {
	n := daysIn(first)
	day := func(d int) time.Time { return first.AddDate(0, 0, d-1) }
	var out []time.Time
	switch {
	case len(r.byMonthDay) > 0:
		for _, md := range r.byMonthDay {
			if md < 0 {
				md = n + md + 1
			}
			if md >= 1 && md <= n && r.matchWeekday(day(md)) {
				out = append(out, day(md))
			}
		}
	case len(r.byDay) > 0:
		for _, w := range r.byDay {
			var all []time.Time
			for d := 1; d <= n; d++ {
				if day(d).Weekday() == w.wd {
					all = append(all, day(d))
				}
			}
			out = append(out, nth(all, w.n)...)
		}
	case startDay <= n:
		out = append(out, day(startDay))
	}
	return out
}

// yearDays expands BYDAY across the whole year starting at jan1, as YEARLY
// rules without BYMONTH do (e.g. "the 20th Monday").
func (r *rrule) yearDays(jan1 time.Time) []time.Time {
	var out []time.Time
	for _, w := range r.byDay {
		var all []time.Time
		for t := jan1; t.Year() == jan1.Year(); t = t.AddDate(0, 0, 1) {
			if t.Weekday() == w.wd {
				all = append(all, t)
			}
		}
		out = append(out, nth(all, w.n)...)
	}
	return out
}

// nth picks the n'th of ts (1-based; negative counts from the end), or all
// of them for 0.
func nth(ts []time.Time, n int) []time.Time {
	switch {
	case n == 0:
		return ts
	case n > 0 && n <= len(ts):
		return ts[n-1 : n]
	case n < 0 && -n <= len(ts):
		return ts[len(ts)+n : len(ts)+n+1]
	}
	return nil
}

func (r *rrule) setPos(ts []time.Time) []time.Time {
	if len(r.bySetPos) == 0 {
		return ts
	}
	var out []time.Time
	for _, p := range r.bySetPos {
		out = append(out, nth(ts, p)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return dedupe(out)
}

func (r *rrule) matchMonth(t time.Time) bool {
	if len(r.byMonth) == 0 {
		return true
	}
	for _, m := range r.byMonth {
		if t.Month() == m {
			return true
		}
	}
	return false
}

func (r *rrule) matchMonthDay(t time.Time) bool {
	if len(r.byMonthDay) == 0 {
		return true
	}
	n := daysIn(t)
	for _, md := range r.byMonthDay {
		if md == t.Day() || md < 0 && n+md+1 == t.Day() {
			return true
		}
	}
	return false
}

// matchWeekday checks BYDAY's weekdays, ignoring ordinals; it's how BYDAY
// limits DAILY rules and BYMONTHDAY.
func (r *rrule) matchWeekday(t time.Time) bool {
	if len(r.byDay) == 0 {
		return true
	}
	for _, w := range r.byDay {
		if t.Weekday() == w.wd {
			return true
		}
	}
	return false
}

func daysIn(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func dedupe(ts []time.Time) []time.Time {
	out := ts[:0]
	for i, t := range ts {
		if i == 0 || !t.Equal(ts[i-1]) {
			out = append(out, t)
		}
	}
	return out
}
