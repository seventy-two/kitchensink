package mail

import (
	"html"
	"io"
	"mime"
	"regexp"
	"strings"

	_ "github.com/emersion/go-message/charset" // decodes other charsets
	gomail "github.com/emersion/go-message/mail"
)

// useful matches subjects of orders, bookings, tickets, appointments,
// deliveries and changes to them. It's deliberately loose: Gemini decides
// what's really worth adding, and marketing that slips through is
// dropped there.
var useful = regexp.MustCompile(`(?i)\b(` + strings.Join([]string{
	`orders?`, `ordered`, `booking`, `booked`, `reservations?`, `reserved`,
	`confirm(ed|ation)?`, `e-?tickets?`, `tickets?`, `appointments?`,
	`deliver(y|ed|ing)`, `dispatched`, `shipped`, `on (its|the) way`, `arriving`,
	`itinerary`, `check-?in`, `your (slot|visit|stay|trip|flight|table)`,
	`cancel+ed`, `cancel+ation`, `resched\w*`, `amended`, `receipt`, `subscription`,
	`returns?`, `refund(ed)?`, `collect(ion)?`,
}, "|") + `)\b`)

// looksUseful reports whether a subject is worth reading.
func looksUseful(subject string) bool { return useful.MatchString(subject) }

// maxText bounds how much of a message Gemini reads, in characters.
const maxText = 15000

// messageText is the readable text of a raw RFC 822 message: its plain
// text part, or its HTML made plain when that's all there is or the plain
// part is a stub ("view this email in your browser").
func messageText(r io.Reader) (string, error) {
	mr, err := gomail.CreateReader(r)
	if err != nil {
		return "", err
	}
	var plain, htm string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if plain != "" || htm != "" {
				break // keep what was read before a broken part
			}
			return "", err
		}
		h, ok := p.Header.(*gomail.InlineHeader)
		if !ok {
			continue // an attachment
		}
		t, _, _ := mime.ParseMediaType(h.Get("Content-Type"))
		if t == "" {
			t = "text/plain"
		}
		b, err := io.ReadAll(io.LimitReader(p.Body, 2<<20))
		if err != nil {
			continue
		}
		switch {
		case t == "text/plain" && plain == "":
			plain = string(b)
		case t == "text/html" && htm == "":
			htm = string(b)
		}
	}
	text := tidy(plain)
	if htm != "" && len(text) < 300 {
		if h := htmlText(htm); len(h) > len(text) {
			text = h
		}
	}
	return clip(text, maxText), nil
}

var (
	dropped  = regexp.MustCompile(`(?is)<(script|style|head|title)\b.*?</(script|style|head|title)\s*>|<!--.*?-->`)
	breaks   = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|tr|li|h[1-6]|table|section|blockquote)\s*>`)
	cells    = regexp.MustCompile(`(?i)</t[dh]\s*>`)
	tags     = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces   = regexp.MustCompile(`[ \t\x{a0}\x{200c}\x{200b}\x{34f}]+`)
	blankRun = regexp.MustCompile(`\n{3,}`)
)

// htmlText makes an HTML email plain: the text with line breaks where
// blocks end, and no markup, styles or scripts.
func htmlText(s string) string {
	s = dropped.ReplaceAllString(s, "")
	s = breaks.ReplaceAllString(s, "\n")
	s = cells.ReplaceAllString(s, " ")
	s = tags.ReplaceAllString(s, "")
	return tidy(html.UnescapeString(s))
}

// tidy trims each line and squeezes out runs of spaces and blank lines.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(spaces.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(blankRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

// clip shortens s to at most n characters.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
