package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {
	dir := t.TempDir()
	creds := filepath.Join(dir, "creds.json")
	token := filepath.Join(dir, "token")
	exp := time.Now().Add(time.Hour).UnixMilli()
	require.NoError(t, os.WriteFile(creds, []byte(`{"claudeAiOauth":{"accessToken":"acc","expiresAt":`+jsonInt(exp)+`}}`), 0o600))
	require.NoError(t, os.WriteFile(token, []byte("ks\n"), 0o600))

	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/usage":
			assert.Equal(t, "Bearer acc", r.Header.Get("Authorization"))
			assert.Equal(t, "oauth-2025-04-20", r.Header.Get("anthropic-beta"))
			io.WriteString(w, `{"five_hour":{"utilization":1},"spend":{"used":{}},"limits":[{"kind":"session","percent":1}]}`)
		case "/act":
			assert.Equal(t, "Bearer ks", r.Header.Get("Authorization"))
			require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		}
	}))
	defer srv.Close()
	require.NoError(t, run(context.Background(), srv.Client(), creds, token, srv.URL+"/usage", srv.URL+"/act"))
	assert.Equal(t, map[string]any{"type": "usage", "data": map[string]any{"limits": []any{map[string]any{"kind": "session", "percent": 1.0}}}}, got,
		"only the limits are sent on")
}

func TestExpiredLogin(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"claudeAiOauth":{"accessToken":"a","expiresAt":1000}}`), 0o600))
	_, err := accessToken(p, time.Now())
	assert.ErrorContains(t, err, "expired")
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
