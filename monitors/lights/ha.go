package lights

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// client talks to Home Assistant's REST API.
type client struct {
	http  *http.Client
	base  string // e.g. "http://homeassistant.local:8123", no trailing slash
	token string
}

// state is one entity's state, as /api/states gives it.
type state struct {
	EntityID   string `json:"entity_id"`
	State      string `json:"state"`
	Attributes struct {
		FriendlyName string `json:"friendly_name"`
		// Brightness is 0-255, or null while the light is off. HA sends
		// it as a float (255.0).
		Brightness          *float64 `json:"brightness"`
		SupportedColorModes []string `json:"supported_color_modes"`
	} `json:"attributes"`
}

// errUnreachable wraps failures to reach Home Assistant at all, as opposed
// to it answering with an error.
var errUnreachable = errors.New("Home Assistant unreachable")

// do sends a request and decodes a JSON answer into out (if not nil), or
// returns the raw body.
func (c *client) do(ctx context.Context, method, path string, body any, out any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error repeats the whole URL; the inner error says enough.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%w: %v", errUnreachable, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: reading answer: %v", errUnreachable, err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("Home Assistant refused the token (%s)", resp.Status)
	case resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable ||
		resp.StatusCode == http.StatusGatewayTimeout:
		// What a tunnel or proxy in front of it says when it can't get
		// through, or HA while it starts up.
		return nil, fmt.Errorf("%w: %s", errUnreachable, resp.Status)
	case resp.StatusCode/100 != 2:
		return nil, fmt.Errorf("Home Assistant: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return nil, fmt.Errorf("Home Assistant: decoding %s: %w", path, err)
		}
	}
	return b, nil
}

// states returns every entity's state.
func (c *client) states(ctx context.Context) ([]state, error) {
	var out []state
	_, err := c.do(ctx, http.MethodGet, "/api/states", nil, &out)
	return out, err
}

// areaLights returns the IDs of the lights in the given areas (names or
// IDs), by rendering a template, since the REST API has no areas of its
// own. Unknown areas have no lights.
func (c *client) areaLights(ctx context.Context, areas []string) (map[string]bool, error) {
	names, err := json.Marshal(areas)
	if err != nil {
		return nil, err
	}
	tmpl := "{{ " + string(names) + ` | map('area_entities') | sum(start=[]) | select('match', 'light\\.') | list | tojson }}`
	b, err := c.do(ctx, http.MethodPost, "/api/template", map[string]string{"template": tmpl}, nil)
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, fmt.Errorf("Home Assistant: reading areas' lights: %w", err)
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// call calls a light service (turn_on, turn_off or toggle) with data,
// which names the entity_id and anything else, e.g. brightness_pct.
func (c *client) call(ctx context.Context, service string, data map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "/api/services/light/"+service, data, nil)
	return err
}
