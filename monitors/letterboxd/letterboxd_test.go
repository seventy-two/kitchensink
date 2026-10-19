package letterboxd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func init() { pagePause = 0 }

var now = time.Date(2026, 10, 14, 21, 0, 0, 0, time.UTC)

// fakeLetterboxd serves the saved page as page 1 and, without its next
// link, as page 2; extra films go at the top of page 1.
func fakeLetterboxd(t *testing.T, extra *string) *httptest.Server {
	return fakeLetterboxdCounting(t, extra, new(int))
}

// fakeLetterboxdCounting is fakeLetterboxd, counting requests for page 2.
func fakeLetterboxdCounting(t *testing.T, extra *string, page2 *int) *httptest.Server {
	b, err := os.ReadFile("testdata/watchlist.html")
	require.NoError(t, err)
	page := string(b)
	last := strings.Replace(page, `<a class="next" href="`, `<a class="nope" href="`, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /someone/watchlist/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(*extra + page))
	})
	mux.HandleFunc("GET /someone/watchlist/page/2/", func(w http.ResponseWriter, r *http.Request) {
		*page2++
		w.Write([]byte(last))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newMonitor(t *testing.T, srv *httptest.Server, local string) *Monitor {
	raw, _ := json.Marshal(Config{User: "someone", Local: local})
	m, err := New(raw)
	require.NoError(t, err)
	m.base = srv.URL
	return m
}

func TestPoll(t *testing.T) {
	extra := ""
	srv := fakeLetterboxd(t, &extra)
	m := newMonitor(t, srv, "watchgrab")
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, res.Events, "the first poll only records")
	require.Len(t, res.Blocks, 1)
	table := res.Blocks[0]
	require.Len(t, table.Rows, 6)
	assert.Len(t, table.Columns, 3)
	first := table.Rows[0]
	assert.Equal(t, "The Third Man", first[0].Text)
	assert.Equal(t, "https://letterboxd.com/film/the-third-man/", first[0].URL)
	assert.Equal(t, "1949", first[1].Text)
	assert.Equal(t, &monitor.Local{App: "watchgrab", Arg: "the-third-man"}, first[2].Local)
	assert.Equal(t, "6 films", res.Summary[0].Value)
	assert.Equal(t, "Amélie", table.Rows[2][0].Text)
	assert.Equal(t, "Newest: The Third Man (1949)", res.Summary[0].Detail)
	require.NotNil(t, res.SummaryBlock)
	assert.Len(t, res.SummaryBlock.Rows, summaryRows)

	extra = `<div class="react-component" data-item-name="Fish Tank (2009)" data-item-slug="fish-tank" >`
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{"added/fish-tank"}, monitortest.Keys(res.Events))
	assert.True(t, res.Events[0].Quiet)
	assert.Equal(t, "Added to the watchlist: Fish Tank (2009)", res.Events[0].Title)
	assert.Equal(t, "Fish Tank", res.Blocks[0].Rows[0][0].Text)
}

func TestNoLocalApp(t *testing.T) {
	extra := ""
	m := newMonitor(t, fakeLetterboxd(t, &extra), "")
	res, err := m.Poll(context.Background(), monitortest.Env(now))
	require.NoError(t, err)
	assert.Len(t, res.Blocks[0].Columns, 2)
	assert.Len(t, res.Blocks[0].Rows[0], 2)
}

func TestNotAWatchlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>Just a moment...</html>"))
	}))
	t.Cleanup(srv.Close)
	_, err := newMonitor(t, srv, "").Poll(context.Background(), monitortest.Env(now))
	assert.Error(t, err)
}

func TestFirstPageBetweenFullReads(t *testing.T) {
	extra, page2 := "", 0
	srv := fakeLetterboxdCounting(t, &extra, &page2)
	m := newMonitor(t, srv, "")
	env := monitortest.Env(now)
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Equal(t, 1, page2, "the first poll reads it all")

	// Ten minutes on, only the first page: the addition goes on top of the
	// list as last read.
	extra = `<div class="react-component" data-item-name="Fish Tank (2009)" data-item-slug="fish-tank" >`
	res, err := m.Poll(context.Background(), at(env, now.Add(10*time.Minute)))
	require.NoError(t, err)
	assert.Equal(t, 1, page2)
	assert.Equal(t, []string{"added/fish-tank"}, monitortest.Keys(res.Events))
	assert.Equal(t, "Fish Tank", res.Blocks[0].Rows[0][0].Text)
	assert.Equal(t, "The Third Man", res.Blocks[0].Rows[1][0].Text)

	// An hour after the last full read, another.
	_, err = m.Poll(context.Background(), at(env, now.Add(time.Hour)))
	require.NoError(t, err)
	assert.Equal(t, 2, page2)
}

// at is env at another time, sharing its state.
func at(env monitor.Env, t time.Time) monitor.Env {
	env.Now = func() time.Time { return t }
	return env
}
