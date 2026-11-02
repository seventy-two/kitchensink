// Package reddit shows the posts in a few subreddits, from Reddit's public
// RSS feeds.
//
// Reddit serves those feeds to programs but allows only about one request
// a minute from an address, so each poll reads one subreddit, in turn, and
// the panel shows the rest as last read.
package reddit

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("reddit", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// Subs are the subreddits, without "r/".
	Subs []string `json:"subs"`
	// Sort is "hot" (the default), "new", "rising" or "top"; for top, Top
	// is the span ("day" by default, or "hour", "week", "month", "year",
	// "all").
	Sort string `json:"sort"`
	Top  string `json:"top"`
	// Rows is how many posts each subreddit shows (default 15).
	Rows int `json:"rows"`
	// Open, if set, names an app on the client's machine that opens a
	// post, given its link (a Reddit reader); clients that have it open
	// posts there rather than in the browser.
	Open string `json:"open"`
	// Hide leaves out posts by these authors, e.g. "AutoModerator" for
	// the stickied daily threads.
	Hide []string `json:"hide"`
}

// Monitor is the reddit monitor.
type Monitor struct {
	cfg  Config
	base string
}

var subRe = regexp.MustCompile(`^[A-Za-z0-9_]{2,21}$`)

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Sort: "hot", Top: "day", Rows: 15}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Subs) == 0 {
		return nil, fmt.Errorf("reddit needs subs")
	}
	for i, s := range c.Subs {
		s = strings.TrimPrefix(strings.TrimPrefix(s, "/"), "r/")
		if !subRe.MatchString(s) {
			return nil, fmt.Errorf("%q isn't a subreddit name", c.Subs[i])
		}
		c.Subs[i] = s
	}
	switch c.Sort {
	case "hot", "new", "rising", "top":
	default:
		return nil, fmt.Errorf("sort must be hot, new, rising or top")
	}
	if c.Rows <= 0 {
		c.Rows = 15
	}
	return &Monitor{cfg: c, base: "https://www.reddit.com"}, nil
}

// post is one post as the feed gives it.
type post struct {
	ID     string    `json:"id"` // t3_…
	Title  string    `json:"title"`
	Author string    `json:"author"`
	URL    string    `json:"url"` // the comments page
	Link   string    `json:"link,omitempty"`
	Time   time.Time `json:"time"`
	Image  string    `json:"image,omitempty"`
}

// sub is a subreddit's posts as last read.
type sub struct {
	Posts []post    `json:"posts"`
	Read  time.Time `json:"read"`
	Err   string    `json:"err,omitempty"` // the last read's, if it failed
}

type feed struct {
	Entries []struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Author  string `xml:"author>name"`
		Content string `xml:"content"`
		Links   []struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Published string `xml:"published"`
		Thumbnail struct {
			URL string `xml:"url,attr"`
		} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
	} `xml:"entry"`
}

// linkRe finds the "[link]" a post's content ends with: where a link
// post goes (for a text post, its own comments page).
var linkRe = regexp.MustCompile(`<a href="([^"]+)">\[link\]</a>`)

// parseFeed reads a subreddit's feed.
func parseFeed(body []byte) ([]post, error) {
	var f feed
	if err := xml.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("reading the feed: %w", err)
	}
	var posts []post
	for _, e := range f.Entries {
		if !strings.HasPrefix(e.ID, "t3_") || len(e.Links) == 0 {
			continue
		}
		p := post{
			ID:     e.ID,
			Title:  stripEmoji(e.Title),
			Author: strings.TrimPrefix(e.Author, "/u/"),
			URL:    e.Links[0].Href,
			Image:  e.Thumbnail.URL,
		}
		p.Time, _ = time.Parse(time.RFC3339, e.Published)
		if m := linkRe.FindStringSubmatch(e.Content); m != nil {
			if l := html.UnescapeString(m[1]); l != p.URL {
				p.Link = l
			}
		}
		posts = append(posts, p)
	}
	return posts, nil
}

func (m *Monitor) feedURL(name string) string {
	u := fmt.Sprintf("%s/r/%s/%s/.rss", m.base, name, m.cfg.Sort)
	if m.cfg.Sort == "top" {
		u += "?t=" + url.QueryEscape(m.cfg.Top)
	}
	return u
}

// userAgent says who's asking, as Reddit asks of programs.
const userAgent = "kitchensink-reddit/1.0 (personal dashboard; github.com/seventy-two/kitchensink)"

func (m *Monitor) read(ctx context.Context, client *http.Client, name string) ([]post, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.feedURL(name), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errBusy
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("r/%s: %s", name, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseFeed(body)
}

// errBusy is Reddit's "too many requests": the subreddit is tried again
// at its next turn.
var errBusy = fmt.Errorf("Reddit asked us to slow down")

// State keys: each subreddit's posts by name, and whose turn it is.
const (
	subsKey = "subs"
	turnKey = "turn"
)

// retryBusy is how soon to poll again after Reddit's said slow down: its
// limit resets each minute.
const retryBusy = 70 * time.Second

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	subs := map[string]sub{}
	if _, err := env.State.Get(subsKey, &subs); err != nil {
		return monitor.Result{}, err
	}
	var turn int
	if _, err := env.State.Get(turnKey, &turn); err != nil {
		return monitor.Result{}, err
	}
	// A subreddit never read goes first; otherwise each in turn.
	name := m.cfg.Subs[turn%len(m.cfg.Subs)]
	for _, s := range m.cfg.Subs {
		if subs[s].Read.IsZero() {
			name = s
			break
		}
	}
	var next time.Duration
	posts, err := m.read(ctx, env.HTTP, name)
	s := subs[name]
	switch {
	case err == nil:
		s = sub{Posts: posts, Read: env.Now()}
		turn++
	case err == errBusy:
		next = retryBusy
	default:
		s.Err = err.Error()
		turn++
	}
	subs[name] = s
	if err := env.State.Set(subsKey, subs); err != nil {
		return monitor.Result{}, err
	}
	if err := env.State.Set(turnKey, turn%len(m.cfg.Subs)); err != nil {
		return monitor.Result{}, err
	}

	res := monitor.Result{Next: next}
	// Short, the top few of each, in one table.
	short := monitor.Block{
		Type:    monitor.BlockTable,
		Columns: []monitor.Column{{Title: "", Fill: true}, {Title: "", Align: monitor.AlignRight}},
		Rows:    [][]monitor.Cell{},
	}
	read := 0
	for _, n := range m.cfg.Subs {
		got, ok := subs[n]
		if !ok || got.Read.IsZero() {
			continue
		}
		read++
		shown := m.visible(got.Posts)
		res.Blocks = append(res.Blocks, m.table(n, shown, env.Now()))
		top := m.table(n, shown[:min(summaryRows, len(shown))], env.Now())
		for _, row := range top.Rows {
			row[0].Detail = "r/" + n + " · " + row[0].Detail
			short.Rows = append(short.Rows, row)
		}
		st := monitor.Stat{Label: "r/" + n, Value: fmt.Sprintf("%d posts", len(shown)), URL: m.subURL(n)}
		if len(shown) > 0 {
			st.Detail = shown[0].Title
		}
		res.Summary = append(res.Summary, st)
	}
	res.SummaryBlock = &short
	if read == 0 {
		if err != nil {
			return monitor.Result{}, err
		}
		return monitor.Result{}, fmt.Errorf("nothing read yet")
	}
	return res, nil
}

// summaryRows is how many of each subreddit's posts the short view shows.
const summaryRows = 3

// visible is the posts shown: not by hidden authors, at most Rows.
func (m *Monitor) visible(posts []post) []post {
	var out []post
	for _, p := range posts {
		hidden := false
		for _, h := range m.cfg.Hide {
			if strings.EqualFold(h, p.Author) {
				hidden = true
			}
		}
		if !hidden {
			out = append(out, p)
		}
		if len(out) == m.cfg.Rows {
			break
		}
	}
	return out
}

func (m *Monitor) subURL(name string) string { return "https://www.reddit.com/r/" + name + "/" }

func (m *Monitor) table(name string, posts []post, now time.Time) monitor.Block {
	b := monitor.Block{
		Type:    monitor.BlockTable,
		Title:   "r/" + name,
		Columns: []monitor.Column{{Title: "", Fill: true}, {Title: "", Align: monitor.AlignRight}},
		Rows:    [][]monitor.Cell{},
	}
	for _, p := range posts {
		b.Rows = append(b.Rows, []monitor.Cell{
			{Text: p.Title, URL: p.URL, Open: m.cfg.Open, Detail: "u/" + p.Author},
			{Text: ago(now.Sub(p.Time)), Tone: monitor.ToneMuted},
		})
	}
	return b
}

// stripEmoji removes emoji, which clients' fonts can't draw, and tidies
// the spaces left behind.
func stripEmoji(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 0x1F000 && r <= 0x1FAFF, // pictographs, emoticons, flags
			r >= 0x2600 && r <= 0x27BF, // symbols and dingbats
			r >= 0x1F1E6 && r <= 0x1F1FF,
			r == 0xFE0F, r == 0x200D, r == 0x20E3:
			continue
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// ago writes a post's age shortly: "5m", "3h", "2d".
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
}
