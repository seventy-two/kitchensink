package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/backend/memory"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-imap/server"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const orderMessage = "From: \"Amazon.co.uk\" <auto-confirm@amazon.co.uk>\r\n" +
	"To: you@example.com\r\n" +
	"Subject: Ordered: Finish dishwasher tablets\r\n" +
	"Date: Mon, 12 Oct 2026 20:00:00 +0100\r\n" +
	"Message-ID: <order-1@amazon.co.uk>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=b\r\n" +
	"\r\n" +
	"--b\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"View in your browser\r\n" +
	"--b\r\n" +
	"Content-Type: text/html; charset=iso-8859-1\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"<html><head><style>p{color:red}</style></head><body><p>Thanks for your order=\r\n" +
	" &amp; welcome.</p><table><tr><td>Finish 100 pack</td><td>=A312.50</td></tr></table></body></html>\r\n" +
	"--b--\r\n"

// imapServer runs go-imap's in-memory server, whose user is "username"
// with password "password", with an order added to its inbox.
func imapServer(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := server.New(memory.New())
	s.AllowInsecureAuth = true
	go s.Serve(l)
	t.Cleanup(func() { s.Close() })

	c, err := client.Dial(l.Addr().String())
	require.NoError(t, err)
	require.NoError(t, c.Login("username", "password"))
	require.NoError(t, c.Append("INBOX", nil, time.Now(), bytes.NewBufferString(orderMessage)))
	c.Logout()
	return l.Addr().String()
}

func TestIMAP(t *testing.T) {
	addr := imapServer(t)
	a, err := newIMAP(Account{Name: "Gmail", Server: addr, User: "username", Password: "pass word"})
	require.NoError(t, err)
	a.plain = true
	env := monitortest.Env(now)
	ctx := context.Background()

	s, err := a.Open(ctx, env)
	require.NoError(t, err)
	hs, cursor, err := s.List("", time.Time{})
	require.NoError(t, err)
	require.Len(t, hs, 2, "the server's own message, and the order")
	h := hs[1]
	assert.Equal(t, "<order-1@amazon.co.uk>", h.ID)
	assert.Equal(t, "Amazon.co.uk <auto-confirm@amazon.co.uk>", h.From)
	assert.Equal(t, "Ordered: Finish dishwasher tablets", h.Subject)
	assert.True(t, h.Date.Equal(time.Date(2026, 10, 12, 20, 0, 0, 0, london)))

	text, err := s.Body(h.Ref)
	require.NoError(t, err)
	assert.Equal(t, "Thanks for your order & welcome.\nFinish 100 pack £12.50", text, "the HTML, since the plain part's a stub")

	// Nothing new after the cursor, even though "n:*" matches the last.
	hs, again, err := s.List(cursor, time.Time{})
	require.NoError(t, err)
	assert.Empty(t, hs)
	assert.Equal(t, cursor, again)
	// A cursor from a rebuilt folder starts again.
	hs, _, err = s.List("999:7", time.Time{})
	require.NoError(t, err)
	assert.Len(t, hs, 2)
	require.NoError(t, s.Close())

	bad, _ := newIMAP(Account{Name: "Gmail", Server: addr, User: "username", Password: "wrong"})
	bad.plain = true
	_, err = bad.Open(ctx, env)
	assert.ErrorContains(t, err, "signing in")
}

// fakeMicrosoft plays Microsoft's sign-in and Graph endpoints.
type fakeMicrosoft struct {
	*httptest.Server
	mu        sync.Mutex
	entered   bool // the user code's been entered
	revoked   bool
	refreshes int
}

func newFakeMicrosoft(t *testing.T) *fakeMicrosoft {
	f := &fakeMicrosoft{}
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /consumers/oauth2/v2.0/devicecode", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		assert.Equal(t, "app-id", r.Form.Get("client_id"))
		assert.Equal(t, scope, r.Form.Get("scope"))
		reply(w, 200, map[string]any{"device_code": "secret-device", "user_code": "ABCD1234",
			"verification_uri": "https://microsoft.com/devicelogin", "expires_in": 900, "interval": 5})
	})
	mux.HandleFunc("POST /consumers/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			assert.Equal(t, "secret-device", r.Form.Get("device_code"))
			if !f.entered {
				reply(w, 400, map[string]any{"error": "authorization_pending", "error_description": "AADSTS70016: pending\r\nTrace ID: x"})
				return
			}
			reply(w, 200, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3600})
		case "refresh_token":
			if f.revoked {
				reply(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			assert.Equal(t, "refresh-1", r.Form.Get("refresh_token"))
			f.refreshes++
			reply(w, 200, map[string]any{"access_token": "access-2", "expires_in": 3600}) // no new refresh token
		}
	})
	mux.HandleFunc("GET /v1.0/me/mailFolders/inbox/messages", func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, []string{"Bearer access-1", "Bearer access-2"}, r.Header.Get("Authorization"))
		q := r.URL.Query()
		if r.URL.Query().Get("page") == "2" {
			reply(w, 200, map[string]any{"value": []any{map[string]any{
				"id": "AAM2", "internetMessageId": "<b@x>", "subject": "Your booking", "receivedDateTime": "2026-10-13T09:00:00Z",
				"from": map[string]any{"emailAddress": map[string]any{"name": "Hotel", "address": "stay@hotel.com"}},
			}}})
			return
		}
		assert.Equal(t, "receivedDateTime ge 2026-10-06T17:00:00Z", q.Get("$filter"))
		assert.Equal(t, "receivedDateTime asc", q.Get("$orderby"))
		reply(w, 200, map[string]any{
			"value": []any{map[string]any{
				"id": "AAM1", "internetMessageId": "<a@x>", "subject": "Order confirmed", "receivedDateTime": "2026-10-12T08:00:00Z",
				"from": map[string]any{"emailAddress": map[string]any{"name": "shop@x.com", "address": "shop@x.com"}},
			}},
			"@odata.nextLink": f.URL + "/v1.0/me/mailFolders/inbox/messages?page=2",
		})
	})
	mux.HandleFunc("GET /v1.0/me/messages/AAM1", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, `outlook.body-content-type="text"`, r.Header.Get("Prefer"))
		reply(w, 200, map[string]any{"body": map[string]any{"contentType": "text", "content": "Order  1\r\n\r\n\r\n\r\nThanks"}})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func TestOutlook(t *testing.T) {
	ms := newFakeMicrosoft(t)
	a, err := newOutlook(Account{Name: "Outlook", ClientID: "app-id"})
	require.NoError(t, err)
	a.loginURL, a.graphURL = ms.URL, ms.URL+"/v1.0"
	env := monitortest.Env(now)
	ctx := context.Background()

	// Not signed in: a code to enter, which stays the same while it's
	// waited for.
	_, err = a.Open(ctx, env)
	var si *signIn
	require.True(t, errors.As(err, &si), err)
	assert.Equal(t, "ABCD1234", si.Code)
	assert.Equal(t, "https://microsoft.com/devicelogin", si.URL)
	assert.Equal(t, now.Add(15*time.Minute), si.Expires)
	assert.Equal(t, 10*time.Second, si.Wait)
	_, err = a.Open(ctx, env)
	require.True(t, errors.As(err, &si))
	assert.Equal(t, "ABCD1234", si.Code)

	// Entered: signed in, and reading mail.
	ms.entered = true
	s, err := a.Open(ctx, env)
	require.NoError(t, err)
	hs, cursor, err := s.List("", now.AddDate(0, 0, -7))
	require.NoError(t, err)
	assert.Equal(t, []header{
		{ID: "<a@x>", Ref: "AAM1", From: "shop@x.com", Subject: "Order confirmed", Date: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)},
		{ID: "<b@x>", Ref: "AAM2", From: "Hotel <stay@hotel.com>", Subject: "Your booking", Date: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)},
	}, hs, "both pages")
	assert.Equal(t, "2026-10-13T09:00:00Z", cursor)
	text, err := s.Body("AAM1")
	require.NoError(t, err)
	assert.Equal(t, "Order 1\n\nThanks", text)

	// An hour on, the access token's refreshed, keeping the refresh token.
	later := at(env, now.Add(time.Hour))
	_, err = a.Open(ctx, later)
	require.NoError(t, err)
	assert.Equal(t, 1, ms.refreshes)
	var tok tokens
	_, err = env.State.Get(a.tokensKey(), &tok)
	require.NoError(t, err)
	assert.Equal(t, "refresh-1", tok.Refresh)
	assert.Equal(t, "access-2", tok.Access)

	// Signed out elsewhere: back to a code.
	ms.revoked = true
	_, err = a.Open(ctx, at(env, now.Add(3*time.Hour)))
	require.True(t, errors.As(err, &si), err)
}

func TestText(t *testing.T) {
	assert.Equal(t, "Hello\nworld", tidy("  Hello \r\n\t world  "))
	assert.Equal(t, "Your order\n\nItem Price\nTea £3", htmlText(`<!-- x --><script>alert(1)</script><h1>Your order</h1><br><br><table><tr><th>Item</th><th>Price</th></tr><tr><td>Tea</td><td>&pound;3</td></tr></table>`))

	plain, err := messageText(strings.NewReader("Subject: x\r\nContent-Type: text/plain\r\n\r\n" + strings.Repeat("All plain. ", 40)))
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(plain, "All plain."))
	assert.Equal(t, "abc…", clip("abcdef", 3))

	for subject, want := range map[string]bool{
		"Your Amazon.co.uk order of \"Finish...\"":     true,
		"Ordered: 2 items":                             true,
		"Booking confirmation - Pizza Express":         true,
		"Your e-ticket for Vildhjarta":                 true,
		"Your appointment on 4 Nov":                    true,
		"Your parcel is on its way":                    true,
		"Your Waitrose delivery slot has been amended": true,
		"Check-in now open":                            true,
		"Weekly newsletter":                            false,
		"Re: lunch?":                                   false,
		"Boxing Day plans":                             false, // "order" isn't inside other words
	} {
		assert.Equal(t, want, looksUseful(subject), subject)
	}
}
