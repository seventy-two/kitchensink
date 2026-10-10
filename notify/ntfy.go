// Package notify holds notifiers that send events outside kitchensink, for
// devices that don't run a kitchensink client.
package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/seventy-two/kitchensink/monitor"
)

// Ntfy publishes events to an ntfy topic (https://ntfy.sh, or self-hosted).
type Ntfy struct {
	// URL is the topic URL, e.g. "https://ntfy.sh/my-topic".
	URL string `json:"url"`
	// Token is an optional access token.
	Token  string       `json:"token"`
	Client *http.Client `json:"-"`
}

// NewNtfy builds an Ntfy notifier from its JSON config.
func NewNtfy(config json.RawMessage) (*Ntfy, error) {
	n := &Ntfy{}
	if err := monitor.Decode(config, n); err != nil {
		return nil, err
	}
	if n.URL == "" {
		return nil, fmt.Errorf("ntfy needs a url")
	}
	n.Client = http.DefaultClient
	return n, nil
}

var ntfyPriority = map[monitor.Priority]string{
	monitor.PriorityLow:    "low",
	monitor.PriorityNormal: "default",
	monitor.PriorityHigh:   "high",
}

func (n *Ntfy) Notify(ctx context.Context, e monitor.Event) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(e.Body))
	if err != nil {
		return err
	}
	// ntfy reads metadata from headers, which must be ASCII; the title goes
	// RFC 2047 encoded so it can hold £ signs and team names.
	req.Header.Set("Title", "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(e.Title))+"?=")
	req.Header.Set("Priority", ntfyPriority[e.Priority])
	req.Header.Set("Tags", e.Monitor)
	if e.URL != "" {
		req.Header.Set("Click", e.URL)
	}
	if e.Image != "" {
		req.Header.Set("Attach", e.Image)
	}
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	resp, err := n.Client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ntfy returned %s", resp.Status)
	}
	return nil
}
