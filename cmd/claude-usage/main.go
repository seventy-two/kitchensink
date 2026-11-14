// Command claude-usage sends the user's Claude plan usage to kitchensink's
// claude monitor. It reads the login Claude Code keeps in
// ~/.claude/.credentials.json (and keeps fresh: this never refreshes it,
// as that would rotate the token under Claude Code), asks Claude for the
// usage /usage shows, and posts only the plan's limits.
//
// It's meant to run from a Claude Code Stop hook, so it's quiet, quick to
// skip (at most one send per -every, tracked by a stamp file) and always
// exits 0 unless the flags are wrong.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const usageURL = "https://api.anthropic.com/api/oauth/usage"

func main() {
	home, _ := os.UserHomeDir()
	creds := flag.String("creds", filepath.Join(home, ".claude", ".credentials.json"), "Claude Code's credentials file")
	tokenFile := flag.String("token", filepath.Join(home, ".config", "kitchensink", "token"), "file holding the kitchensink API token")
	base := flag.String("url", "https://0x48.io/kitchensink", "kitchensink's base URL")
	id := flag.String("monitor", "claude", "the claude monitor's ID")
	every := flag.Duration("every", 2*time.Minute, "send at most this often")
	stamp := flag.String("stamp", filepath.Join(home, ".cache", "claude-usage.stamp"), "file whose time is the last send")
	verbose := flag.Bool("v", false, "say what happened")
	flag.Parse()

	if fi, err := os.Stat(*stamp); err == nil && time.Since(fi.ModTime()) < *every {
		if *verbose {
			fmt.Println("sent recently; skipping")
		}
		return
	}
	// Stamp before sending so overlapping hooks don't all send.
	_ = os.MkdirAll(filepath.Dir(*stamp), 0o755)
	_ = os.WriteFile(*stamp, nil, 0o644)
	now := time.Now()
	_ = os.Chtimes(*stamp, now, now)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := run(ctx, http.DefaultClient, *creds, *tokenFile, usageURL, strings.TrimRight(*base, "/")+"/api/monitors/"+*id+"/actions"); err != nil {
		fmt.Fprintln(os.Stderr, "claude-usage:", err)
		return
	}
	if *verbose {
		fmt.Println("sent")
	}
}

func run(ctx context.Context, hc *http.Client, credsPath, tokenPath, usageURL, actionURL string) error {
	access, err := accessToken(credsPath, time.Now())
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return err
	}
	limits, err := fetchLimits(ctx, hc, usageURL, access)
	if err != nil {
		return err
	}
	return send(ctx, hc, actionURL, strings.TrimSpace(string(raw)), limits)
}

// accessToken reads Claude Code's OAuth access token, refusing an expired
// one (Claude Code refreshes it when it next runs).
func accessToken(path string, now time.Time) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var c struct {
		OAuth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // Unix milliseconds
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("reading credentials: %w", err)
	}
	if c.OAuth.AccessToken == "" {
		return "", errors.New("no Claude login in the credentials file")
	}
	if c.OAuth.ExpiresAt != 0 && now.After(time.UnixMilli(c.OAuth.ExpiresAt)) {
		return "", errors.New("Claude login has expired; Claude Code renews it when next used")
	}
	return c.OAuth.AccessToken, nil
}

// fetchLimits gets the usage and returns its "limits" list as it came.
func fetchLimits(ctx context.Context, hc *http.Client, url, access string) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage: %s", resp.Status)
	}
	var u struct {
		Limits json.RawMessage `json:"limits"`
	}
	if err := json.Unmarshal(body, &u); err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	if len(u.Limits) == 0 || string(u.Limits) == "null" {
		return nil, errors.New("usage has no limits list; its format may have changed")
	}
	return u.Limits, nil
}

// send posts the limits to the monitor as a "usage" action.
func send(ctx context.Context, hc *http.Client, url, token string, limits json.RawMessage) error {
	body, err := json.Marshal(map[string]any{"type": "usage", "data": map[string]json.RawMessage{"limits": limits}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("kitchensink: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
