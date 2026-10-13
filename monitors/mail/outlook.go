package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// outlookAccount reads a Microsoft account's inbox through Microsoft
// Graph. It signs in with the device code flow: the panel shows a code to
// enter at microsoft.com/devicelogin, and the refresh token it gets is
// kept in the monitor's state.
type outlookAccount struct {
	name, clientID, tenant string
	// Endpoints, replaced in tests.
	loginURL, graphURL string
}

func newOutlook(a Account) (*outlookAccount, error) {
	if a.ClientID == "" {
		return nil, fmt.Errorf("outlook needs the clientId of an app registered in Microsoft Entra")
	}
	acc := &outlookAccount{name: a.Name, clientID: a.ClientID, tenant: a.Tenant,
		loginURL: "https://login.microsoftonline.com", graphURL: "https://graph.microsoft.com/v1.0"}
	if acc.tenant == "" {
		acc.tenant = "consumers"
	}
	return acc, nil
}

func (a *outlookAccount) Name() string { return a.name }

// scope is what's asked for: reading mail, and staying signed in.
const scope = "offline_access Mail.Read"

// tokens are the account's sign-in, kept between polls.
type tokens struct {
	Access  string    `json:"access,omitempty"`
	Refresh string    `json:"refresh,omitempty"`
	Expiry  time.Time `json:"expiry,omitzero"`
	// A device code waiting to be entered.
	Device *deviceCode `json:"device,omitempty"`
}

type deviceCode struct {
	Code     string        `json:"code"` // the device code, kept secret
	UserCode string        `json:"userCode"`
	URL      string        `json:"url"`
	Expires  time.Time     `json:"expires"`
	Interval time.Duration `json:"interval"`
}

func (a *outlookAccount) tokensKey() string { return "outlook:" + a.name }

func (a *outlookAccount) Open(ctx context.Context, env monitor.Env) (session, error) {
	var t tokens
	if _, err := env.State.Get(a.tokensKey(), &t); err != nil {
		return nil, err
	}
	save := func() error { return env.State.Set(a.tokensKey(), t) }
	now := env.Now()

	if t.Refresh == "" {
		// Not signed in: show a code, and see whether it's been entered.
		if t.Device == nil || !now.Before(t.Device.Expires) {
			d, err := a.newDeviceCode(ctx, env.HTTP, now)
			if err != nil {
				return nil, err
			}
			t.Device = d
			if err := save(); err != nil {
				return nil, err
			}
		} else {
			tok, err := a.token(ctx, env.HTTP, url.Values{
				"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
				"device_code": {t.Device.Code},
			})
			var oe *oauthError
			switch {
			case errors.As(err, &oe) && (oe.Code == "authorization_pending" || oe.Code == "slow_down"):
			case errors.As(err, &oe) && (oe.Code == "expired_token" || oe.Code == "authorization_declined" || oe.Code == "bad_verification_code"):
				t.Device = nil // a new code next time
				if err := save(); err != nil {
					return nil, err
				}
			case err != nil:
				return nil, err
			default:
				t = tok.tokens(now)
				if err := save(); err != nil {
					return nil, err
				}
			}
		}
		if t.Refresh == "" {
			d := t.Device
			if d == nil {
				return nil, &signIn{URL: "https://microsoft.com/devicelogin", Wait: 10 * time.Second}
			}
			return nil, &signIn{URL: d.URL, Code: d.UserCode, Expires: d.Expires, Wait: max(d.Interval, 10*time.Second)}
		}
	}

	if !now.Before(t.Expiry.Add(-2 * time.Minute)) {
		tok, err := a.token(ctx, env.HTTP, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.Refresh}})
		var oe *oauthError
		if errors.As(err, &oe) && oe.Code == "invalid_grant" {
			// Signed out, or the grant was revoked: sign in again.
			if err := env.State.Set(a.tokensKey(), tokens{}); err != nil {
				return nil, err
			}
			return a.Open(ctx, env)
		}
		if err != nil {
			return nil, fmt.Errorf("refreshing sign-in: %w", err)
		}
		next := tok.tokens(now)
		if next.Refresh == "" {
			next.Refresh = t.Refresh
		}
		t = next
		if err := save(); err != nil {
			return nil, err
		}
	}
	return &graphSession{ctx: ctx, client: env.HTTP, base: a.graphURL, token: t.Access}, nil
}

func (a *outlookAccount) newDeviceCode(ctx context.Context, client *http.Client, now time.Time) (*deviceCode, error) {
	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := a.post(ctx, client, "devicecode", url.Values{"scope": {scope}}, &out); err != nil {
		return nil, fmt.Errorf("starting sign-in: %w", err)
	}
	return &deviceCode{Code: out.DeviceCode, UserCode: out.UserCode, URL: out.VerificationURI,
		Expires: now.Add(time.Duration(out.ExpiresIn) * time.Second), Interval: time.Duration(out.Interval) * time.Second}, nil
}

type tokenReply struct {
	Access    string `json:"access_token"`
	Refresh   string `json:"refresh_token"`
	ExpiresIn int    `json:"expires_in"`
}

func (r tokenReply) tokens(now time.Time) tokens {
	return tokens{Access: r.Access, Refresh: r.Refresh, Expiry: now.Add(time.Duration(r.ExpiresIn) * time.Second)}
}

func (a *outlookAccount) token(ctx context.Context, client *http.Client, form url.Values) (tokenReply, error) {
	form.Set("scope", scope)
	var out tokenReply
	err := a.post(ctx, client, "token", form, &out)
	return out, err
}

// oauthError is an error reply from Microsoft's sign-in endpoints.
type oauthError struct{ Code, Description string }

func (e *oauthError) Error() string {
	d, _, _ := strings.Cut(e.Description, "\r\n") // the rest is trace IDs
	return e.Code + ": " + d
}

func (a *outlookAccount) post(ctx context.Context, client *http.Client, endpoint string, form url.Values, out any) error {
	form.Set("client_id", a.clientID)
	u := a.loginURL + "/" + url.PathEscape(a.tenant) + "/oauth2/v2.0/" + endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return &oauthError{Code: e.Error, Description: e.Description}
		}
		return fmt.Errorf("%s returned %s", endpoint, resp.Status)
	}
	return json.Unmarshal(body, out)
}

// graphSession reads the inbox with an access token.
type graphSession struct {
	ctx    context.Context
	client *http.Client
	base   string
	token  string
}

// maxPages bounds how many pages of messages one List reads; the cursor
// carries on from there next time.
const maxPages = 20

// List's cursor is the receivedDateTime of the last message seen; the
// next List starts there, and messages listed twice are seen already.
func (s *graphSession) List(cursor string, since time.Time) ([]header, string, error) {
	from := since.UTC().Format(time.RFC3339)
	if cursor != "" {
		from = cursor
	}
	q := url.Values{
		"$select":  {"id,internetMessageId,subject,from,receivedDateTime"},
		"$filter":  {"receivedDateTime ge " + from},
		"$orderby": {"receivedDateTime asc"},
		"$top":     {"100"},
	}
	next := s.base + "/me/mailFolders/inbox/messages?" + q.Encode()
	var out []header
	for page := 0; next != "" && page < maxPages; page++ {
		var in struct {
			Value []struct {
				ID               string    `json:"id"`
				InternetID       string    `json:"internetMessageId"`
				Subject          string    `json:"subject"`
				ReceivedDateTime time.Time `json:"receivedDateTime"`
				From             struct {
					EmailAddress struct {
						Name    string `json:"name"`
						Address string `json:"address"`
					} `json:"emailAddress"`
				} `json:"from"`
			} `json:"value"`
			Next string `json:"@odata.nextLink"`
		}
		if err := s.get(next, &in); err != nil {
			if len(out) > 0 {
				break // keep what was read; carry on next time
			}
			return nil, cursor, err
		}
		for _, v := range in.Value {
			id := v.InternetID
			if id == "" {
				id = "graph:" + v.ID
			}
			from := v.From.EmailAddress.Address
			if n := v.From.EmailAddress.Name; n != "" && n != from {
				from = n + " <" + from + ">"
			}
			out = append(out, header{ID: id, Ref: v.ID, From: from, Subject: v.Subject, Date: v.ReceivedDateTime})
			cursor = v.ReceivedDateTime.UTC().Format(time.RFC3339)
		}
		next = in.Next
	}
	if cursor == "" {
		cursor = from
	}
	return out, cursor, nil
}

func (s *graphSession) Body(ref string) (string, error) {
	var in struct {
		Body struct {
			ContentType string `json:"contentType"`
			Content     string `json:"content"`
		} `json:"body"`
	}
	if err := s.get(s.base+"/me/messages/"+url.PathEscape(ref)+"?$select=body", &in); err != nil {
		return "", err
	}
	text := in.Body.Content
	if strings.EqualFold(in.Body.ContentType, "html") {
		text = htmlText(text)
	}
	return clip(tidy(text), maxText), nil
}

func (s *graphSession) get(u string, out any) error {
	req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Prefer", `outlook.body-content-type="text"`)
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("graph returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

func (s *graphSession) Close() error { return nil }
