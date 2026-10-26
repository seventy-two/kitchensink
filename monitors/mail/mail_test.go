package mail

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/seventy-two/kitchensink/monitors/calendar"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var london, _ = time.LoadLocation("Europe/London")

// Tuesday 13 October 2026, 18:00.
var now = time.Date(2026, 10, 13, 18, 0, 0, 0, london)

// fakeAccount serves messages from memory.
type fakeAccount struct {
	name   string
	msgs   []header // in order received
	bodies map[string]string
	err    error
	opened int
}

func (a *fakeAccount) Name() string { return a.name }

func (a *fakeAccount) Open(ctx context.Context, env monitor.Env) (session, error) {
	a.opened++
	if a.err != nil {
		return nil, a.err
	}
	return a, nil
}

func (a *fakeAccount) List(cursor string, since time.Time) ([]header, string, error) {
	var out []header
	n := 0
	fmt.Sscan(cursor, &n)
	for _, h := range a.msgs[n:] {
		if !h.Date.Before(since) {
			out = append(out, h)
		}
	}
	return out, fmt.Sprint(len(a.msgs)), nil
}

func (a *fakeAccount) Body(ref string) (string, error) {
	b, ok := a.bodies[ref]
	if !ok {
		return "", fmt.Errorf("gone")
	}
	return b, nil
}

func (a *fakeAccount) Close() error { return nil }

func (a *fakeAccount) add(subject, from string, at time.Time, body string) {
	ref := fmt.Sprint(len(a.msgs))
	a.msgs = append(a.msgs, header{ID: "<" + ref + "@" + a.name + ">", Ref: ref, From: from, Subject: subject, Date: at})
	if a.bodies == nil {
		a.bodies = map[string]string{}
	}
	a.bodies[ref] = body
}

// fakeGemini answers by the email's subject, and records what it's asked.
type fakeGemini struct {
	*httptest.Server
	mu      sync.Mutex
	answers map[string]extraction
	asked   []string
	fail    bool
	status  int // of a failure; 503 if unset
}

var subjectLine = regexp.MustCompile(`(?m)^Subject: (.*)$`)

func newFakeGemini(t *testing.T) *fakeGemini {
	g := &fakeGemini{answers: map[string]extraction{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail {
			http.Error(w, "busy", cmp.Or(g.status, http.StatusServiceUnavailable))
			return
		}
		subject := subjectLine.FindStringSubmatch(req.Input)[1]
		g.asked = append(g.asked, req.Input)
		x, ok := g.answers[subject]
		if !ok {
			x = extraction{Kind: "other", Summary: "Nothing"}
		}
		text, _ := json.Marshal(x)
		json.NewEncoder(w).Encode(map[string]any{"steps": []any{map[string]any{
			"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}},
		}}})
	}))
	t.Cleanup(g.Close)
	return g
}

// actions records what the monitor sends to others.
type actions struct {
	mu   sync.Mutex
	sent []string // "monitor type id text data"
	fail map[string]error
}

func (a *actions) env(env monitor.Env) monitor.Env {
	env.Act = func(ctx context.Context, id string, act monitor.Action) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		if err := a.fail[id]; err != nil {
			return err
		}
		var parts []string
		for _, p := range []string{id, act.Type, act.ID, act.Text, string(act.Data)} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		a.sent = append(a.sent, strings.Join(parts, " "))
		return nil
	}
	return env
}

func newMonitor(t *testing.T, g *fakeGemini, accounts ...account) *Monitor {
	t.Helper()
	m, err := New(json.RawMessage(`{
		"accounts": [{"type": "imap", "name": "x", "user": "u", "password": "p"}],
		"gemini": {"apiKey": "k", "url": "` + g.URL + `"},
		"choresDone": {"waitrose-order": "a Waitrose groceries order being placed"},
		"ignoreFrom": ["newsletter@"]
	}`))
	require.NoError(t, err)
	m.accounts = accounts
	return m
}

func at(env monitor.Env, t time.Time) monitor.Env {
	env.Now = func() time.Time { return t }
	return env
}

func TestReadsAndPassesOn(t *testing.T) {
	g := newFakeGemini(t)
	gmail := &fakeAccount{name: "Gmail"}
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, london) }
	gmail.add("Your tickets for Vildhjarta", "Ticketmaster <tm@ticketmaster.co.uk>", day(1, 10), "2 tickets, Thu 16 Oct, doors 18:30")
	gmail.add("Big sale: order now!", "Shop <newsletter@shop.com>", day(2, 10), "ignored sender")
	gmail.add("Lunch on Friday?", "Mum <mum@example.com>", day(3, 10), "not an order")
	gmail.add("Your Waitrose order is confirmed", "Waitrose <noreply@waitrose.co.uk>", day(12, 9), "Delivery Wed 14 Oct 10:00-11:00")
	gmail.add("Ordered: Finish dishwasher tablets", "Amazon.co.uk <auto-confirm@amazon.co.uk>", day(12, 20), "Finish 100 pack")
	gmail.add("Your return is due", "Amazon.co.uk <returns@amazon.co.uk>", day(13, 8), "Return the boots by 30 Oct")
	gmail.add("Order confirmed: socks", "Shop <orders@shop.com>", day(13, 9), "a one-off")
	g.answers["Your tickets for Vildhjarta"] = extraction{Kind: "ticket", Summary: "Vildhjarta tickets", Events: []xEvent{{
		Key: "ticketmaster-123", Title: "Vildhjarta", Date: "2026-10-16", Start: "19:00", End: "23:00",
		Location: "The Fleece, Bristol", Detail: "2 tickets · doors 18:30",
	}, {
		// No end time, as with most cinema tickets: still to come, so kept.
		Key: "odeon-wjz85b3", Title: "Street Fighter", Date: "2026-10-16", Start: "20:40", Location: "ODEON, Bristol",
	}}}
	g.answers["Your Waitrose order is confirmed"] = extraction{Kind: "order", Summary: "Waitrose delivery", Chores: []string{"waitrose-order", "made-up"},
		Events: []xEvent{{Key: "waitrose-55", Title: "Waitrose delivery", Date: "2026-10-14", Start: "10:00", End: "11:00"}}}
	g.answers["Ordered: Finish dishwasher tablets"] = extraction{Kind: "order", Summary: "Amazon order: dishwasher tablets",
		Routine: []xItem{{Item: "dishwasher tablets", LastsDays: 60}}}
	g.answers["Your return is due"] = extraction{Kind: "other", Summary: "Amazon return", Todos: []string{"Return the boots to Amazon by Fri 30 Oct"}}

	m := newMonitor(t, g, gmail)
	m.perPoll = 2
	acts := &actions{}
	env := acts.env(monitortest.Env(now))

	// The first poll reads the oldest few; the rest follow quickly.
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, backlogPoll, res.Next)
	for res.Next == backlogPoll {
		res, err = m.Poll(context.Background(), env)
		require.NoError(t, err)
	}
	assert.Len(t, g.asked, 5, "only orders, bookings and the like, not the newsletter or Mum")
	assert.Contains(t, g.asked[1], "- waitrose-order: a Waitrose groceries order being placed")
	assert.Contains(t, g.asked[0], "Today is Tuesday 13 October 2026")

	assert.Equal(t, []string{
		`calendar upsert {"key":"mail:ticketmaster-123","title":"Vildhjarta","start":"2026-10-16T19:00:00+01:00","end":"2026-10-16T23:00:00+01:00","location":"The Fleece, Bristol","detail":"2 tickets · doors 18:30","source":"Email"}`,
		`calendar upsert {"key":"mail:odeon-wjz85b3","title":"Street Fighter","start":"2026-10-16T20:40:00+01:00","location":"ODEON, Bristol","source":"Email"}`,
		`calendar upsert {"key":"mail:waitrose-55","title":"Waitrose delivery","start":"2026-10-14T10:00:00+01:00","end":"2026-10-14T11:00:00+01:00","source":"Email"}`,
		`chores done waitrose-order {"day":"2026-10-12"}`,
		`chores track reorder-dishwasher-tablets {"name":"Dishwasher tablets","every":60,"last":"2026-10-12"}`,
		`todo add Return the boots to Amazon by Fri 30 Oct`,
	}, acts.sent)

	// The panel lists what was found, newest first; the summary leads
	// with the latest. Only the recent ones raised (quiet) events.
	require.Len(t, res.Blocks, 1)
	b := res.Blocks[0]
	assert.Equal(t, "Found in email", b.Title)
	assert.Equal(t, monitor.Item{Title: "Amazon return", Subtitle: "To do: Return the boots to Amazon by Fri 30 Oct", Detail: "Gmail · Amazon.co.uk · 08:00"}, b.Items[0])
	assert.Equal(t, "Calendar: Vildhjarta, Fri 16 Oct 19:00 · Calendar: Street Fighter, Fri 16 Oct 20:40", b.Items[3].Subtitle)
	assert.Len(t, b.Items, 4, "the one-off order added nothing")
	assert.Equal(t, "Amazon return", res.Summary[0].Value)
	assert.True(t, res.Background, "kept off the dashboard")

	// Nothing new: nothing asked.
	n := len(g.asked)
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, g.asked, n)
	assert.Zero(t, res.Next)
}

// A parcel's emails come from the shop (order number) and the courier
// (tracking number); the dispatch notice has both, so all share one event.
func TestLinksParcelEmails(t *testing.T) {
	g := newFakeGemini(t)
	mb := &fakeAccount{name: "Outlook"}
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, london) }
	mb.add("Your Royal Mail parcel is on its way", "Royal Mail <no-reply@royalmail.com>", day(14, 7), "due Thu")
	mb.add("Order update: Final Fantasy VIII", "eBay <ebay@ebay.com>", day(14, 8), "posted")
	mb.add("OUT FOR DELIVERY: Final Fantasy VIII", "eBay <ebay@ebay.com>", day(15, 9), "today")
	mb.add("Your Royal Mail parcel is due to be delivered today", "Royal Mail <no-reply@royalmail.com>", day(15, 10), "today")
	g.answers["Your Royal Mail parcel is on its way"] = extraction{Kind: "delivery", Refs: []string{"VU793801544GB"},
		Events: []xEvent{{Key: "royal-mail-vu793801544gb", Title: "Royal Mail parcel", Date: "2026-10-15", Start: "10:05", End: "14:05"}}}
	g.answers["Order update: Final Fantasy VIII"] = extraction{Kind: "delivery", Refs: []string{"11-15308-85027", "VU 7938 0154 4GB", "1"}}
	g.answers["OUT FOR DELIVERY: Final Fantasy VIII"] = extraction{Kind: "delivery", Refs: []string{"11-15308-85027"},
		Events: []xEvent{{Key: "ebay-11-15308-85027", Title: "eBay delivery: Final Fantasy VIII", Date: "2026-10-15"}}}
	g.answers["Your Royal Mail parcel is due to be delivered today"] = extraction{Kind: "delivery", Refs: []string{"VU793801544GB"},
		Events: []xEvent{{Key: "royal-mail-vu793801544gb", Title: "Royal Mail parcel", Date: "2026-10-15", Start: "10:00", End: "14:00"}}}

	m := newMonitor(t, g, mb)
	acts := &actions{}
	env := acts.env(monitortest.Env(day(15, 11)))
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{
		`calendar upsert {"key":"mail:royal-mail-vu793801544gb","title":"Royal Mail parcel","start":"2026-10-15T10:05:00+01:00","end":"2026-10-15T14:05:00+01:00","source":"Email"}`,
		// eBay's says what it is but not when: the window's kept.
		`calendar upsert {"key":"mail:royal-mail-vu793801544gb","title":"eBay delivery: Final Fantasy VIII","start":"2026-10-15T10:05:00+01:00","end":"2026-10-15T14:05:00+01:00","source":"Email"}`,
		// Royal Mail's narrows the window; eBay's title stays.
		`calendar upsert {"key":"mail:royal-mail-vu793801544gb","title":"eBay delivery: Final Fantasy VIII","start":"2026-10-15T10:00:00+01:00","end":"2026-10-15T14:00:00+01:00","source":"Email"}`,
	}, acts.sent)
}

func TestMergeEvent(t *testing.T) {
	rm := xEvent{Title: "Royal Mail parcel", Date: "2026-10-21", Start: "10:00", End: "14:00", Detail: "tracking VU1"}
	ebay := xEvent{Title: "eBay delivery: Final Fantasy VIII", Date: "2026-10-21"}
	assert.Equal(t, xEvent{Title: "eBay delivery: Final Fantasy VIII", Date: "2026-10-21", Start: "10:00", End: "14:00", Detail: "tracking VU1"}, mergeEvent(rm, ebay))
	// Put off a day: the old day's window doesn't carry over.
	moved := xEvent{Title: "Royal Mail parcel", Date: "2026-10-22"}
	assert.Equal(t, moved, mergeEvent(rm, moved))
	// Came a day early: still the shop's title, so it says what's come.
	early := xEvent{Title: "Amazon parcel", Date: "2026-10-20", Delivered: true}
	assert.Equal(t, xEvent{Title: "eBay delivery: Final Fantasy VIII", Date: "2026-10-20", Delivered: true}, mergeEvent(ebay, early))
}

func TestLinkRefsJoinsEvents(t *testing.T) {
	links := map[string]link{}
	k, merged := linkRefs(links, []string{"11-15308-85027"}, "ebay-11-15308-85027", "2026-10-14")
	assert.Equal(t, "ebay-11-15308-85027", k)
	assert.Empty(t, merged)
	k, _ = linkRefs(links, []string{"VU793801544GB"}, "royal-mail-vu793801544gb", "2026-10-14")
	assert.Equal(t, "royal-mail-vu793801544gb", k)
	// One email with both: the second event joins the first.
	k, merged = linkRefs(links, []string{"11-15308-85027", "VU793801544GB"}, "", "2026-10-15")
	assert.Equal(t, "ebay-11-15308-85027", k)
	assert.Equal(t, []string{"royal-mail-vu793801544gb"}, merged)
	assert.Equal(t, "ebay-11-15308-85027", links["VU793801544GB"].Key)
	// Nothing known and no key of its own: the first reference.
	k, _ = linkRefs(links, []string{"AB-123456"}, "", "2026-10-15")
	assert.Equal(t, "ab123456", k)
	// Forgotten once old.
	linked := map[string]xEvent{"ab123456": {Title: "x"}}
	pruneLinks(links, linked, time.Date(2027, 3, 1, 0, 0, 0, 0, london))
	assert.Empty(t, links)
	assert.Empty(t, linked)
}

func TestReorderInterval(t *testing.T) {
	g := newFakeGemini(t)
	amazon := &fakeAccount{name: "Gmail"}
	for i, d := range []time.Time{
		time.Date(2026, 5, 1, 9, 0, 0, 0, london),
		time.Date(2026, 6, 10, 9, 0, 0, 0, london),
		time.Date(2026, 6, 11, 9, 0, 0, 0, london), // a second order the day after: the same purchase
		time.Date(2026, 7, 26, 9, 0, 0, 0, london),
		time.Date(2026, 9, 1, 9, 0, 0, 0, london),
	} {
		s := fmt.Sprintf("Ordered: coffee %d", i)
		amazon.add(s, "Amazon <auto-confirm@amazon.co.uk>", d, "beans")
		g.answers[s] = extraction{Kind: "order", Summary: "Coffee", Routine: []xItem{{Item: "Coffee beans", LastsDays: 14}}}
	}
	m := newMonitor(t, g, amazon)
	acts := &actions{}
	env := acts.env(monitortest.Env(now))
	for range 3 {
		_, err := m.Poll(context.Background(), env)
		require.NoError(t, err)
	}
	require.Len(t, acts.sent, 5)
	assert.Equal(t, `chores track reorder-coffee-beans {"name":"Coffee beans","every":14,"last":"2026-05-01"}`, acts.sent[0], "Gemini's guess, until it's bought again")
	assert.Equal(t, `chores track reorder-coffee-beans {"name":"Coffee beans","every":40,"last":"2026-06-10"}`, acts.sent[1])
	assert.Equal(t, `chores track reorder-coffee-beans {"name":"Coffee beans","every":40,"last":"2026-06-11"}`, acts.sent[2])
	assert.Equal(t, `chores track reorder-coffee-beans {"name":"Coffee beans","every":40,"last":"2026-09-01"}`, acts.sent[4], "the median of 40, 46 and 37 days")
}

func TestPastAndCancelled(t *testing.T) {
	g := newFakeGemini(t)
	acc := &fakeAccount{name: "Outlook"}
	acc.add("Booking confirmed: Dentist", "Dentist <x@dentist.co.uk>", now.AddDate(0, -2, 0), "")
	acc.add("Booking cancelled", "Hotel <x@hotel.com>", now.Add(-time.Hour), "")
	acc.add("Your booking: Lisbon", "Hotel <x@hotel.com>", now.Add(-time.Minute), "")
	acc.add("Old return reminder", "Shop <x@shop.com>", now.AddDate(0, 0, -20), "")
	g.answers["Booking confirmed: Dentist"] = extraction{Kind: "appointment", Summary: "Dentist", Events: []xEvent{{Key: "d", Title: "Dentist", Date: "2026-08-20", Start: "09:00"}}}
	g.answers["Booking cancelled"] = extraction{Kind: "cancellation", Summary: "Hotel cancelled", Cancelled: []string{"Hotel-ABC"}}
	g.answers["Your booking: Lisbon"] = extraction{Kind: "booking", Summary: "Lisbon hotel", Events: []xEvent{
		{Key: "hotel-xyz", Title: "Hotel in Lisbon", Date: "2026-11-02", EndDate: "2026-11-05", Start: "15:00"},
		{Key: "", Title: "Bad date", Date: "2 Nov"},
	}}
	g.answers["Old return reminder"] = extraction{Kind: "other", Summary: "Return", Todos: []string{"Return it"}}
	m := newMonitor(t, g, acc)
	acts := &actions{}
	env := acts.env(monitortest.Env(now))
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"calendar remove mail:hotel-abc",
		`calendar upsert {"key":"mail:hotel-xyz","title":"Hotel in Lisbon","start":"2026-11-02T00:00:00Z","end":"2026-11-06T00:00:00Z","allDay":true,"source":"Email"}`,
	}, acts.sent, "nothing for the past appointment or the old to-do; the stay is all day over its nights")
	var keys []string
	for _, e := range res.Events {
		keys = append(keys, e.Key)
		assert.True(t, e.Quiet)
	}
	assert.Equal(t, []string{"found:<1@Outlook>", "found:<2@Outlook>"}, keys)
}

func TestFailures(t *testing.T) {
	g := newFakeGemini(t)
	good := &fakeAccount{name: "Gmail"}
	good.add("Your order", "Shop <x@shop.com>", now.Add(-time.Hour), "")
	good.msgs = append(good.msgs, header{ID: "<gone>", Ref: "gone", Subject: "Order gone", Date: now.Add(-time.Minute)})
	bad := &fakeAccount{name: "Outlook", err: fmt.Errorf("no route to host")}
	waiting := &fakeAccount{name: "Hotmail", err: &signIn{URL: "https://microsoft.com/devicelogin", Code: "ABCD-1234", Expires: now.Add(15 * time.Minute), Wait: 10 * time.Second}}
	m := newMonitor(t, g, good, bad, waiting)
	env := (&actions{}).env(monitortest.Env(now))

	g.fail = true
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	var texts []string
	for _, b := range res.Blocks {
		texts = append(texts, b.Title+": "+b.Text)
	}
	assert.Equal(t, []string{
		"Outlook: Couldn't check: no route to host",
		"Sign in to Hotmail: Go to https://microsoft.com/devicelogin and enter ABCD-1234 (by 18:15).",
		": Couldn't read mail: Gemini: returned 503 Service Unavailable: busy",
		": Reading 2 emails.",
	}, texts)
	assert.Equal(t, monitor.Stat{Label: "Sign in to Hotmail", Value: "ABCD-1234", Detail: "https://microsoft.com/devicelogin", Tone: monitor.ToneWarn}, res.Summary[0])
	assert.False(t, res.Background, "on the dashboard while the code's needed")
	assert.Equal(t, 10*time.Second, res.Next, "back soon to see if it's signed in, not hammering Gemini")
	require.Len(t, res.Events, 1)
	assert.Equal(t, "signin:Hotmail:2026-10-13", res.Events[0].Key)
	assert.False(t, res.Events[0].Quiet)

	// Without the sign-in waiting, a failure retries in a minute.
	m.accounts = []account{good}
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, retryPoll, res.Next)
	m.accounts = []account{good, bad, waiting}

	// Gemini's back: the message that's gone is skipped, the other read.
	g.fail = false
	res, err = m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, g.asked, 1)
	assert.NotContains(t, res.Blocks[len(res.Blocks)-1].Text, "Reading")
}

func TestConfig(t *testing.T) {
	for _, bad := range []string{
		`{}`,
		`{"gemini": {"apiKey": "k"}}`,
		`{"gemini": {"apiKey": "k"}, "accounts": [{"type": "pop3", "name": "a"}]}`,
		`{"gemini": {"apiKey": "k"}, "accounts": [{"type": "imap", "name": "a"}]}`,
		`{"gemini": {"apiKey": "k"}, "accounts": [{"type": "outlook", "name": "a"}]}`,
		`{"gemini": {"apiKey": "k"}, "accounts": [{"type": "imap", "user": "u", "password": "p"}]}`,
		`{"gemini": {"apiKey": "k"}, "accounts": [{"type": "imap", "name": "a", "user": "u", "password": "p"}, {"type": "outlook", "name": "a", "clientId": "c"}]}`,
		`{"accounts": [{"type": "imap", "name": "a", "user": "u", "password": "p"}]}`,
	} {
		_, err := New(json.RawMessage(bad))
		assert.Error(t, err, bad)
	}
	m, err := New(json.RawMessage(`{"gemini": {"apiKey": "k"}, "todo": "", "accounts": [{"type": "imap", "name": "Gmail", "user": "u", "password": "abcd efgh ijkl mnop"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "", m.todo)
	assert.Equal(t, "calendar", m.calendar)
	assert.Equal(t, "abcdefghijklmnop", m.accounts[0].(*imapAccount).password, "Google shows app passwords with spaces")
}

func TestSkipsWhatGeminiCantRead(t *testing.T) {
	g := newFakeGemini(t)
	acc := &fakeAccount{name: "Gmail"}
	acc.add("Order: huge", "Shop <x@shop.com>", now.Add(-2*time.Hour), "")
	acc.add("Order: fine", "Shop <x@shop.com>", now.Add(-time.Hour), "")
	g.answers["Order: fine"] = extraction{Kind: "order", Summary: "Fine", Todos: []string{"Do it"}}
	m := newMonitor(t, g, acc)
	acts := &actions{}
	env := acts.env(monitortest.Env(now))
	// Each poll tries the first, fails and stops; the last try skips it
	// and goes on to the next, which fails too.
	g.fail = true
	for range maxAttempts {
		_, err := m.Poll(context.Background(), env)
		require.NoError(t, err)
	}
	g.fail = false
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, g.asked, 1, "only the second was asked about again")
	assert.Equal(t, []string{"todo add Do it"}, acts.sent)
	assert.Zero(t, res.Next)
}

func TestOutOfCreditSkipsNothing(t *testing.T) {
	g := newFakeGemini(t)
	acc := &fakeAccount{name: "Gmail"}
	acc.add("Order: fine", "Shop <x@shop.com>", now.Add(-time.Hour), "")
	g.answers["Order: fine"] = extraction{Kind: "order", Summary: "Fine", Todos: []string{"Do it"}}
	m := newMonitor(t, g, acc)
	acts := &actions{}
	env := acts.env(monitortest.Env(now))
	g.fail, g.status = true, http.StatusPaymentRequired
	for range maxAttempts * 2 {
		_, err := m.Poll(context.Background(), env)
		require.NoError(t, err)
	}
	g.fail = false
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Equal(t, []string{"todo add Do it"}, acts.sent, "read once Gemini's back")
}

func TestOver(t *testing.T) {
	today := time.Date(2026, 10, 13, 0, 0, 0, 0, london)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, london) }
	assert.False(t, over(calendar.Added{Start: at(13, 20)}, today), "tonight, with no end")
	assert.True(t, over(calendar.Added{Start: at(12, 20)}, today))
	assert.False(t, over(calendar.Added{Start: at(12, 23), End: at(13, 1)}, today), "ran past midnight into today")
	assert.True(t, over(calendar.Added{Start: at(12, 9), End: at(12, 10)}, today))
}

func TestReadAgainReplacesFinding(t *testing.T) {
	g := newFakeGemini(t)
	acc := &fakeAccount{name: "Gmail"}
	acc.add("Your booking", "Hotel <x@hotel.com>", now.Add(-time.Hour), "")
	g.answers["Your booking"] = extraction{Kind: "booking", Summary: "Hotel", Todos: []string{"Pay"}}
	m := newMonitor(t, g, acc)
	env := (&actions{}).env(monitortest.Env(now))
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	require.NoError(t, env.State.Set(seenKey, map[string]string{}))
	require.NoError(t, env.State.Set(cursorsKey, map[string]string{}))
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Len(t, res.Blocks[0].Items, 1)
}

// A parcel that's come is said at once, with a to-do to fetch it, and its
// calendar event moves to the day it came.
func TestDeliveredParcel(t *testing.T) {
	g := newFakeGemini(t)
	mb := &fakeAccount{name: "Outlook"}
	day := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, london) }
	mb.add("Dispatched: 'Monster Juiced Ripper 500...'", "Amazon.co.uk <shipment-tracking@amazon.co.uk>", day(25, 7), "arriving Tuesday")
	mb.add("Delivered: 'Monster Juiced Ripper 500...'", "Amazon.co.uk <order-update@amazon.co.uk>", day(26, 13), "left with reception")
	g.answers["Dispatched: 'Monster Juiced Ripper 500...'"] = extraction{Kind: "delivery", Refs: []string{"026-5383085-1196347"},
		Events: []xEvent{{Key: "amazon-026-5383085-1196347", Title: "Amazon parcel: Monster Juiced Ripper", Date: "2026-10-27"}}}
	g.answers["Delivered: 'Monster Juiced Ripper 500...'"] = extraction{Kind: "delivery", Summary: "Amazon parcel delivered", Refs: []string{"026-5383085-1196347"},
		Events: []xEvent{{Key: "amazon-026-5383085-1196347", Title: "Amazon parcel", Date: "2026-10-26", Start: "13:05", Detail: "left with reception", Delivered: true}}}

	m := newMonitor(t, g, mb)
	acts := &actions{}
	res, err := m.Poll(context.Background(), acts.env(monitortest.Env(day(26, 14))))
	require.NoError(t, err)
	assert.Equal(t, []string{
		`calendar upsert {"key":"mail:amazon-026-5383085-1196347","title":"Amazon parcel: Monster Juiced Ripper","start":"2026-10-27T00:00:00Z","end":"2026-10-28T00:00:00Z","allDay":true,"source":"Email"}`,
		`calendar upsert {"key":"mail:amazon-026-5383085-1196347","title":"Amazon parcel: Monster Juiced Ripper","start":"2026-10-26T13:05:00Z","detail":"Delivered · left with reception","source":"Email"}`,
		`todo add Pick up the parcel: Amazon parcel: Monster Juiced Ripper`,
	}, acts.sent)
	var notified []monitor.Event
	for _, e := range res.Events {
		if !e.Quiet {
			notified = append(notified, e)
		}
	}
	require.Len(t, notified, 1)
	assert.Equal(t, "Delivered: Amazon parcel: Monster Juiced Ripper", notified[0].Title)
	assert.Equal(t, "left with reception", notified[0].Body)
	assert.Equal(t, monitor.PriorityNormal, notified[0].Priority)

	// One read days later, catching up, says nothing.
	mb2 := &fakeAccount{name: "Outlook"}
	mb2.add("Delivered: 'Survive Style 5+'", "Amazon.co.uk <order-update@amazon.co.uk>", day(23, 13), "left with reception")
	g.answers["Delivered: 'Survive Style 5+'"] = extraction{Kind: "delivery", Summary: "Amazon parcel delivered",
		Events: []xEvent{{Key: "amazon-026-1", Title: "Amazon parcel: Survive Style 5+", Date: "2026-10-23", Delivered: true}}}
	acts = &actions{}
	res, err = newMonitor(t, g, mb2).Poll(context.Background(), acts.env(monitortest.Env(day(26, 14))))
	require.NoError(t, err)
	assert.Empty(t, acts.sent)
	assert.Empty(t, res.Events)
}
