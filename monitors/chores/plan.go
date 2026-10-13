package chores

import (
	"maps"
	"sort"
	"time"
)

// day is a calendar day, counted from 1 January 1970.
type day int

// dayOf is the day t falls on in loc.
func dayOf(t time.Time, loc *time.Location) day {
	y, m, d := t.In(loc).Date()
	return day(time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix() / 86400)
}

// date is the day at midnight UTC, for formatting and weekdays.
func (d day) date() time.Time { return time.Unix(int64(d)*86400, 0).UTC() }

func (d day) String() string { return d.date().Format(time.DateOnly) }

func parseDay(s string) (day, bool) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return 0, false
	}
	return day(t.Unix() / 86400), true
}

// job is one chore as the planner sees it.
type job struct {
	idx   int     // its place in the config, which breaks ties
	due   day     // when it falls due: the last time plus its interval, or today if never done
	flex  int     // how many days either side of due it can move without counting as late
	from  day     // the earliest day it can go: today, or later if put off
	days  [7]bool // preferred weekdays, unless any
	any   bool    // no preferred days
	uses  string  // what it shares with other chores, e.g. the washing machine
	never bool    // never done, so due today only to get it started
}

// Costs of a day, against the job's due day. Lateness past the flex costs
// far more than moving within it, and a day the chore doesn't prefer costs
// about as much as moving it half a week.
const (
	costPerDay     = 1
	costPerLateDay = 10
	costWrongDay   = 4
	searchDays     = 28 // how far past its earliest day a job looks for room
)

// plan puts each job on a day, the most pressing first: each takes the
// cheapest day from its earliest on where what it uses still has room, so
// chores sharing the washing machine spread out instead of piling up.
// used counts what's already been used on each day (today's finished
// loads); perDay is how many of each thing a day allows, 1 if unlisted.
func plan(jobs []job, used map[string]map[day]int, perDay map[string]int) map[int]day {
	order := make([]job, len(jobs))
	copy(order, jobs)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if da, db := a.due+day(a.flex), b.due+day(b.flex); da != db {
			return da < db
		}
		if a.due != b.due {
			return a.due < b.due
		}
		return a.idx < b.idx
	})
	load := map[string]map[day]int{}
	for k, m := range used {
		load[k] = maps.Clone(m)
	}
	room := func(uses string, d day) bool {
		if uses == "" {
			return true
		}
		limit, ok := perDay[uses]
		if !ok {
			limit = 1
		}
		return load[uses][d] < limit
	}

	out := map[int]day{}
	for _, j := range order {
		start := j.from
		if early := j.due - day(j.flex); !j.never && early > start {
			start = early
		}
		best, bestCost := day(0), -1
		for d := start; d <= start+searchDays; d++ {
			if !room(j.uses, d) {
				continue
			}
			if c := j.cost(d); bestCost < 0 || c < bestCost {
				best, bestCost = d, c
			}
		}
		if bestCost < 0 { // everything's full: just go on the first day
			best = start
		}
		out[j.idx] = best
		if j.uses != "" {
			if load[j.uses] == nil {
				load[j.uses] = map[day]int{}
			}
			load[j.uses][best]++
		}
	}
	return out
}

// cost is how bad a day is for the job.
func (j job) cost(d day) int {
	c := 0
	switch {
	case d < j.due:
		c = int(j.due-d) * costPerDay
	case d > j.due:
		late := int(d - j.due)
		c = late * costPerDay
		// A chore never done isn't late yet, just not started.
		if over := late - j.flex; over > 0 && !j.never {
			c += over * costPerLateDay
		}
	}
	if !j.any && !j.days[d.date().Weekday()] {
		c += costWrongDay
	}
	return c
}
