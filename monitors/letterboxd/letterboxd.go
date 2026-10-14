// Package letterboxd shows a Letterboxd user's watchlist, newest additions
// first, from their public watchlist pages.
package letterboxd

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("letterboxd", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// User is the Letterboxd username whose watchlist to show.
	User string `json:"user"`
	// Local, if set, names an app on the client's machine that can fetch a
	// film, given its Letterboxd slug (watchgrab): each row then has a
	// button for it on clients that have the app.
	Local string `json:"local"`
}

// Monitor is the letterboxd monitor.
type Monitor struct {
	cfg  Config
	base string
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.User == "" {
		return nil, fmt.Errorf("letterboxd needs a user")
	}
	return &Monitor{cfg: c, base: "https://letterboxd.com"}, nil
}

// film is one watchlist entry.
type film struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Year int    `json:"year,omitempty"`
}

var (
	posterRe   = regexp.MustCompile(`<div[^>]*data-item-slug="[^"]*"[^>]*>`)
	attrRe     = regexp.MustCompile(`data-item-(slug|name)="([^"]*)"`)
	nameYearRe = regexp.MustCompile(`^(.*) \((\d{4})\)$`)
	nextRe     = regexp.MustCompile(`<a class="next" href="`)
)

// parsePage reads the films on a watchlist page and whether there's
// another.
func parsePage(body string) ([]film, bool) {
	var films []film
	for _, tag := range posterRe.FindAllString(body, -1) {
		var f film
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			if v := html.UnescapeString(m[2]); m[1] == "slug" {
				f.Slug = v
			} else {
				f.Name = v
			}
		}
		if m := nameYearRe.FindStringSubmatch(f.Name); m != nil {
			f.Name = m[1]
			f.Year, _ = strconv.Atoi(m[2])
		}
		if f.Slug != "" {
			films = append(films, f)
		}
	}
	return films, nextRe.MatchString(body)
}

const maxPages = 100

// watchlist reads every page, newest additions first (Letterboxd's
// default order).
func (m *Monitor) watchlist(ctx context.Context, client *http.Client) ([]film, error) {
	var all []film
	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("%s/%s/watchlist/", m.base, m.cfg.User)
		if page > 1 {
			u += fmt.Sprintf("page/%d/", page)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(pagePause):
			}
		}
		body, err := get(ctx, client, u)
		if err != nil {
			return nil, err
		}
		films, more := parsePage(body)
		if len(films) == 0 && !strings.Contains(body, "watchlist") {
			return nil, fmt.Errorf("page %d isn't a watchlist page", page)
		}
		all = append(all, films...)
		if !more {
			return all, nil
		}
	}
	return nil, fmt.Errorf("watchlist has more than %d pages", maxPages)
}

// pagePause keeps the requests gentle; tests set it to zero.
var pagePause = time.Second

func get(ctx context.Context, client *http.Client, u string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (kitchensink)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}

const seenKey = "seen"

// summaryRows is how many of the newest films the short view shows.
const summaryRows = 3

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	films, err := m.watchlist(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}

	// Films added since the last poll are quiet events; the first poll
	// only records what's there.
	var seen []string
	had, err := env.State.Get(seenKey, &seen)
	if err != nil {
		return monitor.Result{}, err
	}
	known := map[string]bool{}
	for _, s := range seen {
		known[s] = true
	}
	var events []monitor.Event
	slugs := make([]string, 0, len(films))
	for _, f := range films {
		slugs = append(slugs, f.Slug)
		if had && !known[f.Slug] {
			events = append(events, monitor.Event{
				Key:   "added/" + f.Slug,
				Title: "Added to the watchlist: " + title(f),
				URL:   m.filmURL(f),
				Quiet: true,
			})
		}
	}
	// Remember removals too, so a film added again is news again.
	if err := env.State.Set(seenKey, slugs); err != nil {
		return monitor.Result{}, err
	}

	table := m.table(films)
	short := m.table(films[:min(summaryRows, len(films))])
	summary := []monitor.Stat{{Label: "Watchlist", Value: plural(len(films), "film")}}
	if len(films) > 0 {
		summary[0].Detail = "Newest: " + title(films[0])
	}
	return monitor.Result{
		Blocks:       []monitor.Block{table},
		Summary:      summary,
		SummaryBlock: &short,
		Subtitle:     plural(len(films), "film") + ", newest first",
		Events:       events,
	}, nil
}

func (m *Monitor) table(films []film) monitor.Block {
	b := monitor.Block{
		Type:    monitor.BlockTable,
		Columns: []monitor.Column{{Title: "Film", Fill: true}, {Title: "Year", Align: monitor.AlignRight}},
		Rows:    [][]monitor.Cell{},
	}
	if m.cfg.Local != "" {
		b.Columns = append(b.Columns, monitor.Column{Title: ""})
	}
	for _, f := range films {
		row := []monitor.Cell{{Text: f.Name, URL: m.filmURL(f)}, {Text: year(f), Tone: monitor.ToneMuted}}
		if m.cfg.Local != "" {
			row = append(row, monitor.Cell{Local: &monitor.Local{App: m.cfg.Local, Arg: f.Slug}})
		}
		b.Rows = append(b.Rows, row)
	}
	return b
}

func (m *Monitor) filmURL(f film) string { return "https://letterboxd.com/film/" + f.Slug + "/" }

func title(f film) string {
	if f.Year == 0 {
		return f.Name
	}
	return fmt.Sprintf("%s (%d)", f.Name, f.Year)
}

func year(f film) string {
	if f.Year == 0 {
		return ""
	}
	return strconv.Itoa(f.Year)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
