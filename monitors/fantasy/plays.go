package fantasy

import (
	"fmt"
	"slices"
	"strings"
)

// play is the part of one of ESPN's play-by-play entries the monitor uses.
type play struct {
	ID       string `json:"id"`
	Sequence string `json:"sequenceNumber"`
	Text     string `json:"text"`
	Short    string `json:"shortText"`
	Type     struct {
		Text string `json:"text"`
	} `json:"type"`
	Yards    int  `json:"statYardage"`
	Turnover bool `json:"isTurnover"`
	Scoring  bool `json:"scoringPlay"`
	Period   struct {
		Number int `json:"number"`
	} `json:"period"`
	Clock struct {
		Display string `json:"displayValue"`
	} `json:"clock"`
	// Start's team is the one with the ball.
	Start struct {
		Team ref `json:"team"`
	} `json:"start"`
	Participants []struct {
		Role    string `json:"type"`
		Athlete ref    `json:"athlete"`
	} `json:"participants"`
}

// ref is an ESPN API link; its last path segment is the ID.
type ref struct {
	Ref string `json:"$ref"`
}

// id is the ID at the end of a link such as
// ".../athletes/4361579?lang=en".
func (r ref) id() string {
	s, _, _ := strings.Cut(r.Ref, "?")
	return s[strings.LastIndex(s, "/")+1:]
}

// owned is a player on one or more of the user's teams.
type owned struct {
	Name    string   `json:"name"`
	Team    string   `json:"team"`
	Leagues []string `json:"leagues"` // short names
}

// Kinds of significant play.
const (
	kindTD        = "TD"
	kindBig       = "Big play"
	kindINT       = "INT thrown"
	kindFumble    = "Fumble lost"
	kindFG        = "Long FG"
	kindMissed    = "Missed kick"
	kindDefTD     = "D/ST TD"
	kindTakeaway  = "D/ST takeaway"
	kindSack      = "D/ST sack"
	kindSafety    = "D/ST safety"
	longFieldGoal = 50
)

// significance is what a play meant for the user's teams.
type significance struct {
	Kind    string
	Good    bool     // good for the user; turnovers and misses aren't
	Who     []string // the user's players (or defence) involved
	Leagues []string // short names of the leagues they're in
}

// classify says what a play meant to the user, if anything: defence is
// the abbreviation of the team without the ball. players are the user's,
// by ESPN athlete ID; defences their team defences, by team abbreviation.
// A play can matter twice over, e.g. an interception thrown by one of the
// user's quarterbacks to one of their defences.
func classify(p play, defence string, players map[string]owned, defences map[string][]string, bigYards int) []significance {
	var out []significance
	if s, ok := defensive(p, defence, defences); ok {
		out = append(out, s)
	}
	if s, ok := offensive(p, players, bigYards); ok {
		out = append(out, s)
	}
	return out
}

// defensive is what the play meant for the user's team defence, if it was
// on the field.
func defensive(p play, defence string, defences map[string][]string) (significance, bool) {
	leagues, ok := defences[defence]
	if !ok {
		return significance{}, false
	}
	typ := p.Type.Text
	s := significance{Good: true, Who: []string{defence + " D/ST"}, Leagues: leagues}
	switch {
	case p.Scoring && strings.Contains(typ, "Touchdown") && (p.Turnover || strings.Contains(typ, "Return")):
		s.Kind = kindDefTD
	case typ == "Safety":
		s.Kind = kindSafety
	case p.Turnover:
		s.Kind = kindTakeaway
	case typ == "Sack":
		s.Kind = kindSack
	default:
		return s, false
	}
	return s, true
}

// offensive is what the play meant for the user's players in it.
func offensive(p play, players map[string]owned, bigYards int) (significance, bool) {
	typ := p.Type.Text
	roles := map[string][]string{} // athlete ID -> roles in the play
	for _, x := range p.Participants {
		id := x.Athlete.id()
		if _, ok := players[id]; ok {
			roles[id] = append(roles[id], x.Role)
		}
	}
	has := func(id string, want ...string) bool {
		return slices.ContainsFunc(roles[id], func(r string) bool { return slices.Contains(want, r) })
	}
	var s significance
	add := func(id string) {
		o := players[id]
		if slices.Contains(s.Who, o.Name) {
			return
		}
		s.Who = append(s.Who, o.Name)
		s.Leagues = union(s.Leagues, o.Leagues)
	}
	ids := make([]string, 0, len(roles))
	for _, x := range p.Participants { // in ESPN's order: passer first
		if id := x.Athlete.id(); roles[id] != nil && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}

	for _, id := range ids {
		switch {
		case p.Scoring && strings.Contains(typ, "Touchdown") && has(id, "passer", "rusher", "receiver", "scorer", "returner"):
			s.Kind, s.Good = kindTD, true
		case strings.Contains(typ, "Interception") && has(id, "passer"):
			s.Kind = kindINT
		case p.Turnover && has(id, "fumbler"):
			s.Kind = kindFumble
		case typ == "Field Goal Good" && has(id, "kicker") && p.Yards >= longFieldGoal:
			s.Kind, s.Good = kindFG, true
		case (strings.Contains(typ, "Field Goal Missed") || strings.Contains(typ, "Blocked Field Goal") ||
			strings.Contains(strings.ToUpper(p.Text), "EXTRA POINT IS NO GOOD")) && has(id, "kicker", "patScorer"):
			s.Kind = kindMissed
		case p.Yards >= bigYards && !p.Turnover && has(id, "passer", "rusher", "receiver"):
			s.Kind, s.Good = kindBig, true
		default:
			continue
		}
		break
	}
	if s.Kind == "" {
		return s, false
	}
	// Everyone of the user's who had a hand in it, e.g. both ends of a
	// touchdown pass.
	for _, id := range ids {
		if has(id, "passer", "rusher", "receiver", "scorer", "returner", "kicker", "fumbler") {
			add(id)
		}
	}
	return s, true
}

// describe is the play in a line, preferring ESPN's short summary.
func describe(p play) string {
	if s := strings.Join(strings.Fields(p.Short), " "); s != "" {
		return s
	}
	text := strings.TrimPrefix(p.Text, "(Shotgun) ")
	if i := strings.Index(text, ". "); i > 0 {
		text = text[:i]
	}
	return text
}

// when is the quarter and clock, e.g. "Q2 7:42" or "OT 3:10".
func when(p play) string {
	q := fmt.Sprintf("Q%d", p.Period.Number)
	if p.Period.Number > 4 {
		q = "OT"
	}
	return strings.TrimSpace(q + " " + p.Clock.Display)
}

func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, x := range b {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}
