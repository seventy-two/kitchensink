package mail

import (
	"fmt"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// blocks are the panel: any account waiting to be signed in or failing,
// what's left to read, and what recent emails added.
func (m *Monitor) blocks(statuses map[string]status, queue []header, found []finding, readErr string, now time.Time) []monitor.Block {
	var out []monitor.Block
	for _, a := range m.accounts {
		st := statuses[a.Name()]
		switch {
		case st.SignIn != nil:
			out = append(out, monitor.Block{Type: monitor.BlockText, Title: "Sign in to " + a.Name(),
				Text: fmt.Sprintf("Go to %s and enter %s (by %s).", st.SignIn.URL, st.SignIn.Code, st.SignIn.Expires.In(m.loc).Format("15:04"))})
		case st.Error != "":
			out = append(out, monitor.Block{Type: monitor.BlockText, Title: a.Name(), Text: "Couldn't check: " + st.Error})
		}
	}
	if readErr != "" {
		out = append(out, monitor.Block{Type: monitor.BlockText, Text: "Couldn't read mail: " + readErr})
	}
	if len(queue) > 0 {
		out = append(out, monitor.Block{Type: monitor.BlockText, Text: fmt.Sprintf("Reading %d %s.", len(queue), plural(len(queue), "email", "emails"))})
	}
	if len(found) == 0 {
		if len(queue) == 0 {
			out = append(out, monitor.Block{Type: monitor.BlockText, Text: "Nothing found in email yet."})
		}
		return out
	}
	b := monitor.Block{Type: monitor.BlockList, Title: "Found in email"}
	for _, f := range found {
		b.Items = append(b.Items, monitor.Item{
			Title:    f.Summary,
			Subtitle: strings.Join(f.Did, " · "),
			Detail:   f.Account + " · " + sender(f.From) + " · " + ago(f.Time.In(m.loc), now.In(m.loc)),
		})
	}
	return append(out, b)
}

// summary is a sign-in needed, or else the latest finding.
func (m *Monitor) summary(statuses map[string]status, queue []header, found []finding) []monitor.Stat {
	for _, a := range m.accounts {
		if si := statuses[a.Name()].SignIn; si != nil {
			return []monitor.Stat{{Label: "Sign in to " + a.Name(), Value: si.Code, Detail: si.URL, Tone: monitor.ToneWarn}}
		}
	}
	if len(found) > 0 {
		return []monitor.Stat{{Label: "From email", Value: found[0].Summary, Detail: strings.Join(found[0].Did, " · ")}}
	}
	if len(queue) > 0 {
		return []monitor.Stat{{Label: "Email", Value: fmt.Sprintf("Reading %d", len(queue)), Tone: monitor.ToneMuted}}
	}
	return []monitor.Stat{{Label: "Email", Value: "Nothing found yet", Tone: monitor.ToneMuted}}
}

// sender is the name in a From address, or the address.
func sender(from string) string {
	if i := strings.Index(from, "<"); i > 0 {
		return strings.Trim(strings.TrimSpace(from[:i]), `"`)
	}
	return from
}

// ago says when, briefly: the time today, else the day.
func ago(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if now.Sub(t) < 6*24*time.Hour {
		return t.Format("Mon")
	}
	return t.Format("2 Jan")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
