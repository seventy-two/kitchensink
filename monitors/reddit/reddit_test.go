package reddit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)

func TestParseFeed(t *testing.T) {
	b, err := os.ReadFile("testdata/hot.rss")
	require.NoError(t, err)
	posts, err := parseFeed(b)
	require.NoError(t, err)
	require.Len(t, posts, 3)

	assert.Equal(t, "t3_1xlqg22", posts[0].ID)
	assert.Equal(t, "AutoModerator", posts[0].Author)
	assert.Equal(t, "https://www.reddit.com/r/2007scape/comments/1xlqg22/have_a_question_about_the_game_or_the_subreddit/", posts[0].URL)
	assert.Empty(t, posts[0].Link, "a text post links to itself")
	assert.Equal(t, time.Date(2026, 11, 2, 7, 1, 32, 0, time.UTC), posts[0].Time.UTC())

	assert.Equal(t, "Deep Sea Diving & Burial At Sea Quest", posts[1].Title)
	assert.Equal(t, "https://secure.runescape.com/m=news/deep-sea-diving-expansion?oldschool=1", posts[1].Link)
	assert.NotEmpty(t, posts[1].Image)
}

func TestNewChecks(t *testing.T) {
	_, err := New([]byte(`{}`))
	assert.Error(t, err)
	_, err = New([]byte(`{"subs":["no spaces"]}`))
	assert.Error(t, err)
	_, err = New([]byte(`{"subs":["a1"],"sort":"best"}`))
	assert.Error(t, err)
	m, err := New([]byte(`{"subs":["r/2007scape","/r/bristol"],"sort":"top","top":"week"}`))
	require.NoError(t, err)
	assert.Equal(t, []string{"2007scape", "bristol"}, m.cfg.Subs)
	assert.Equal(t, "https://www.reddit.com/r/bristol/top/.rss?t=week", m.feedURL("bristol"))
}

// fakeReddit serves the fixture for any subreddit, or answers status
// while busy is set; it records the paths asked for.
type fakeReddit struct {
	mu    sync.Mutex
	paths []string
	agent string
	busy  bool
}

func (f *fakeReddit) serve(t *testing.T) *httptest.Server {
	b, err := os.ReadFile("testdata/hot.rss")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.Path)
		f.agent = r.UserAgent()
		if f.busy {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPollTakesTurns(t *testing.T) {
	f := &fakeReddit{}
	srv := f.serve(t)
	m, err := New([]byte(`{"subs":["2007scape","bristol"],"rows":2,"open":"lurk","hide":["automoderator"]}`))
	require.NoError(t, err)
	m.base = srv.URL
	env := monitortest.Env(now)

	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks, 1, "only the first has been read")
	b := res.Blocks[0]
	assert.Equal(t, "r/2007scape", b.Title)
	require.Len(t, b.Rows, 2)
	assert.Equal(t, "Deep Sea Diving & Burial At Sea Quest", b.Rows[0][0].Text, "AutoModerator's hidden")
	assert.Equal(t, "lurk", b.Rows[0][0].Open)
	assert.Contains(t, b.Rows[0][0].URL, "/r/2007scape/comments/")
	assert.Equal(t, "u/", b.Rows[0][0].Detail[:2])
	assert.Contains(t, f.agent, "kitchensink")

	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.Len(t, res.Blocks, 2)
	assert.Equal(t, "r/bristol", res.Blocks[1].Title)
	require.Len(t, res.Summary, 2)
	require.NotNil(t, res.SummaryBlock)
	require.Len(t, res.SummaryBlock.Rows, 4, "two of each, as rows is 2")
	assert.Equal(t, "lurk", res.SummaryBlock.Rows[3][0].Open)
	assert.Contains(t, res.SummaryBlock.Rows[3][0].Detail, "r/bristol · u/")

	m.Poll(context.Background(), env)
	assert.Equal(t, []string{"/r/2007scape/hot/.rss", "/r/bristol/hot/.rss", "/r/2007scape/hot/.rss"}, f.paths)
}

func TestPollBusyKeepsLastRead(t *testing.T) {
	f := &fakeReddit{}
	srv := f.serve(t)
	m, err := New([]byte(`{"subs":["2007scape"]}`))
	require.NoError(t, err)
	m.base = srv.URL
	env := monitortest.Env(now)

	f.busy = true
	_, err = m.Poll(context.Background(), env)
	assert.ErrorIs(t, err, errBusy, "nothing to show yet")

	f.busy = false
	_, err = m.Poll(context.Background(), env)
	require.NoError(t, err)

	f.busy = true
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks[0].Rows, 3)
	assert.Equal(t, retryBusy, res.Next, "tries again once the limit's reset")
}

func TestStripEmoji(t *testing.T) {
	assert.Equal(t, "14 bags of litter cleared!", stripEmoji("\U0001F44F\U0001F3FD 14 bags of litter cleared!"))
	assert.Equal(t, "An autumn in Bristol (no bridges)", stripEmoji("An autumn in Bristol \U0001F342 (no bridges)"))
	assert.Equal(t, "Café – £5", stripEmoji("Café – £5 \u2764\ufe0f"))
}

func TestAgo(t *testing.T) {
	assert.Equal(t, "now", ago(10*time.Second))
	assert.Equal(t, "5m", ago(5*time.Minute))
	assert.Equal(t, "47h", ago(47*time.Hour))
	assert.Equal(t, "3d", ago(80*time.Hour))
}
