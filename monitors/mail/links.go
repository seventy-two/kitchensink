package mail

import (
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
)

// linksKey holds the references (order, booking and tracking numbers)
// seen together in an email, so emails about one thing from different
// senders share a calendar event. A parcel's shop sends the order number,
// its courier the tracking number, and usually one email (the dispatch
// notice) has both.
const linksKey = "links"

// linkedKey holds the last event each linked key was given, so a later
// email that says less (eBay's "out for delivery" has no time) doesn't
// lose what an earlier one said (the courier's delivery window).
const linkedKey = "linked"

// keepLinkDays is how long a reference is remembered after it was last
// seen.
const keepLinkDays = 120

// link is the event key a reference belongs to.
type link struct {
	Key  string `json:"key"`
	Seen string `json:"seen"` // the day, YYYY-MM-DD
}

// normRef is a reference as it's matched: letters and digits only, in
// upper case. Short ones ("1", "A2") are too likely to clash, so they're
// dropped.
func normRef(r string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(r) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(c)
		}
	}
	if b.Len() < 6 {
		return ""
	}
	return b.String()
}

// linkRefs ties an email's references together and returns the key its
// event should use: the key any of them already has, else own (the key
// Gemini gave), else one made from the first reference. It also returns
// the keys of other events these references now join, which the caller
// replaces with this one.
func linkRefs(links map[string]link, refs []string, own string, day string) (key string, merged []string) {
	var norm []string
	for _, r := range refs {
		if n := normRef(r); n != "" && !slices.Contains(norm, n) {
			norm = append(norm, n)
		}
	}
	if len(norm) == 0 {
		return own, nil
	}
	for _, r := range norm {
		if l, ok := links[r]; ok {
			if key == "" {
				key = l.Key
			} else if l.Key != key && !slices.Contains(merged, l.Key) {
				merged = append(merged, l.Key)
			}
		}
	}
	if key == "" {
		key = own
	}
	if key == "" {
		key = slug(norm[0])
	}
	for r, l := range links {
		if slices.Contains(merged, l.Key) {
			links[r] = link{Key: key, Seen: l.Seen}
		}
	}
	for _, r := range norm {
		links[r] = link{Key: key, Seen: day}
	}
	return key, merged
}

// mergeEvent is a linked event as a new email describes it, keeping what
// the earlier one said that this one doesn't: on the same day, its times
// and detail when this has none, and the longer title (the shop's, which
// names what's coming, over the courier's "Royal Mail parcel").
func mergeEvent(prev, e xEvent) xEvent {
	if prev.Date != e.Date {
		return e // moved: the new day's what counts
	}
	if strings.TrimSpace(e.Start) == "" {
		e.Start, e.End = prev.Start, prev.End
	}
	if strings.TrimSpace(e.Detail) == "" {
		e.Detail = prev.Detail
	}
	if len(strings.TrimSpace(prev.Title)) > len(strings.TrimSpace(e.Title)) {
		e.Title = prev.Title
	}
	if strings.TrimSpace(e.Location) == "" {
		e.Location = prev.Location
	}
	return e
}

// pruneLinks forgets references not seen for keepLinkDays, and events
// no reference leads to.
func pruneLinks(links map[string]link, linked map[string]xEvent, now time.Time) {
	cutoff := now.AddDate(0, 0, -keepLinkDays).Format(time.DateOnly)
	maps.DeleteFunc(links, func(_ string, l link) bool { return l.Seen < cutoff })
	keys := map[string]bool{}
	for _, l := range links {
		keys[l.Key] = true
	}
	maps.DeleteFunc(linked, func(k string, _ xEvent) bool { return !keys[k] })
}

// loadLinks reads the links and linked events, or empty sets.
func loadLinks(env monitor.Env) (map[string]link, map[string]xEvent, error) {
	links, linked := map[string]link{}, map[string]xEvent{}
	if _, err := env.State.Get(linksKey, &links); err != nil {
		return nil, nil, err
	}
	_, err := env.State.Get(linkedKey, &linked)
	return links, linked, err
}

func saveLinks(env monitor.Env, links map[string]link, linked map[string]xEvent) error {
	if err := env.State.Set(linksKey, links); err != nil {
		return err
	}
	return env.State.Set(linkedKey, linked)
}
