package calendar

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// prop is one iCalendar content line: NAME;PARAM=VALUE:value.
type prop struct {
	Name   string
	Params map[string]string
	Value  string
}

// vevent is a VEVENT's properties. A name can repeat (EXDATE, RDATE).
type vevent map[string][]prop

func (e vevent) get(name string) (prop, bool) {
	ps := e[name]
	if len(ps) == 0 {
		return prop{}, false
	}
	return ps[0], true
}

func (e vevent) text(name string) string {
	p, _ := e.get(name)
	return unescape(p.Value)
}

// parseICS reads the VEVENTs from an iCalendar file. Other components
// (VTIMEZONE, VTODO, VALARM inside an event) are skipped.
func parseICS(r io.Reader) ([]vevent, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var (
		events []vevent
		cur    vevent
		depth  int // nesting inside the current VEVENT, e.g. a VALARM
	)
	handle := func(line string) {
		p, ok := parseLine(line)
		if !ok {
			return
		}
		switch {
		case p.Name == "BEGIN" && strings.EqualFold(p.Value, "VEVENT") && cur == nil:
			cur = vevent{}
		case p.Name == "BEGIN" && cur != nil:
			depth++
		case p.Name == "END" && cur != nil && depth > 0:
			depth--
		case p.Name == "END" && cur != nil && strings.EqualFold(p.Value, "VEVENT"):
			events = append(events, cur)
			cur = nil
		case cur != nil && depth == 0:
			cur[p.Name] = append(cur[p.Name], p)
		}
	}

	// Lines are folded by starting the continuation with a space or tab.
	var line strings.Builder
	for sc.Scan() {
		s := strings.TrimRight(sc.Text(), "\r")
		if len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
			line.WriteString(s[1:])
			continue
		}
		if line.Len() > 0 {
			handle(line.String())
		}
		line.Reset()
		line.WriteString(s)
	}
	if line.Len() > 0 {
		handle(line.String())
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

// parseLine splits a content line into its name, parameters and value.
// Parameter values may be quoted, and quoted values may hold ':' and ';'.
func parseLine(line string) (prop, bool) {
	p := prop{}
	i := strings.IndexAny(line, ";:")
	if i <= 0 {
		return p, false
	}
	p.Name = strings.ToUpper(line[:i])
	rest := line[i:]
	for len(rest) > 0 && rest[0] == ';' {
		rest = rest[1:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			return p, false
		}
		key := strings.ToUpper(rest[:eq])
		rest = rest[eq+1:]
		var val string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return p, false
			}
			val = rest[1 : end+1]
			rest = rest[end+2:]
		} else {
			end := strings.IndexAny(rest, ";:")
			if end < 0 {
				return p, false
			}
			val, rest = rest[:end], rest[end:]
		}
		if p.Params == nil {
			p.Params = map[string]string{}
		}
		p.Params[key] = val
	}
	if !strings.HasPrefix(rest, ":") {
		return p, false
	}
	p.Value = rest[1:]
	return p, true
}

// unescape undoes TEXT value escaping.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// parseTime reads a DATE or DATE-TIME property. Dates come back as
// midnight in loc with allDay set. Times are in their TZID's zone, UTC when
// they end in Z, and loc when they're floating.
func parseTime(p prop, loc *time.Location) (t time.Time, allDay bool, err error) {
	v := strings.TrimSpace(p.Value)
	if p.Params["VALUE"] == "DATE" || len(v) == 8 {
		t, err = time.ParseInLocation("20060102", v, loc)
		return t, true, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err = time.Parse("20060102T150405Z", v)
		return t, false, err
	}
	zone := loc
	if id := p.Params["TZID"]; id != "" {
		zone = lookupZone(id, loc)
	}
	t, err = time.ParseInLocation("20060102T150405", v, zone)
	return t, false, err
}

// parseTimes reads a property that may hold a comma-separated list, as
// EXDATE and RDATE do.
func parseTimes(p prop, loc *time.Location) []time.Time {
	var out []time.Time
	for _, v := range strings.Split(p.Value, ",") {
		q := p
		q.Value = v
		if t, _, err := parseTime(q, loc); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// windowsZones maps the Windows time zone names Outlook and Exchange put in
// TZID to IANA names. It covers the zones a UK calendar is likely to meet.
var windowsZones = map[string]string{
	"GMT Standard Time":              "Europe/London",
	"Greenwich Standard Time":        "Atlantic/Reykjavik",
	"UTC":                            "UTC",
	"Coordinated Universal Time":     "UTC",
	"W. Europe Standard Time":        "Europe/Berlin",
	"Romance Standard Time":          "Europe/Paris",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw",
	"GTB Standard Time":              "Europe/Bucharest",
	"FLE Standard Time":              "Europe/Kiev",
	"E. Europe Standard Time":        "Europe/Chisinau",
	"Russian Standard Time":          "Europe/Moscow",
	"Turkey Standard Time":           "Europe/Istanbul",
	"Israel Standard Time":           "Asia/Jerusalem",
	"Arabian Standard Time":          "Asia/Dubai",
	"India Standard Time":            "Asia/Kolkata",
	"China Standard Time":            "Asia/Shanghai",
	"Singapore Standard Time":        "Asia/Singapore",
	"Tokyo Standard Time":            "Asia/Tokyo",
	"AUS Eastern Standard Time":      "Australia/Sydney",
	"New Zealand Standard Time":      "Pacific/Auckland",
	"Eastern Standard Time":          "America/New_York",
	"Central Standard Time":          "America/Chicago",
	"Mountain Standard Time":         "America/Denver",
	"US Mountain Standard Time":      "America/Phoenix",
	"Pacific Standard Time":          "America/Los_Angeles",
	"Atlantic Standard Time":         "America/Halifax",
	"E. South America Standard Time": "America/Sao_Paulo",
}

// lookupZone resolves a TZID: an IANA name, a Windows name, or an IANA name
// behind a prefix such as "/mozilla.org/20050126_1/Europe/London". Anything
// else, such as Outlook's "Customized Time Zone", falls back to def.
func lookupZone(id string, def *time.Location) *time.Location {
	id = strings.Trim(id, `"`)
	if l, err := time.LoadLocation(id); err == nil {
		return l
	}
	if name, ok := windowsZones[id]; ok {
		if l, err := time.LoadLocation(name); err == nil {
			return l
		}
	}
	parts := strings.Split(id, "/")
	for i := 1; i < len(parts)-1; i++ {
		if l, err := time.LoadLocation(strings.Join(parts[i:], "/")); err == nil {
			return l
		}
	}
	return def
}

// parseDuration reads an iCalendar DURATION such as "PT1H30M", "P1D" or
// "-P1W". Days and weeks are counted as 24 hours; callers that care about
// calendar days add them to a date instead.
func parseDuration(s string) (time.Duration, error) {
	orig := s
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("bad duration %q", orig)
	}
	s = s[1:]
	var d time.Duration
	inTime := false
	num := ""
	for _, c := range s {
		switch {
		case c == 'T':
			inTime = true
		case c >= '0' && c <= '9':
			num += string(c)
		default:
			n, err := strconv.Atoi(num)
			if err != nil {
				return 0, fmt.Errorf("bad duration %q", orig)
			}
			num = ""
			unit := map[rune]time.Duration{'W': 7 * 24 * time.Hour, 'D': 24 * time.Hour}
			if inTime {
				unit = map[rune]time.Duration{'H': time.Hour, 'M': time.Minute, 'S': time.Second}
			}
			u, ok := unit[c]
			if !ok {
				return 0, fmt.Errorf("bad duration %q", orig)
			}
			d += time.Duration(n) * u
		}
	}
	if num != "" {
		return 0, fmt.Errorf("bad duration %q", orig)
	}
	if neg {
		d = -d
	}
	return d, nil
}
