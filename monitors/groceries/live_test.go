package groceries

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/require"
)

// TestLiveGemini reads a real receipt with the real Gemini API, then asks
// for recipes; it runs only with GROCERIES_LIVE_GEMINI_KEY set, and
// GROCERIES_LIVE_PDF naming a receipt (and GROCERIES_LIVE_TEXT its email).
func TestLiveGemini(t *testing.T) {
	key, pdf := os.Getenv("GROCERIES_LIVE_GEMINI_KEY"), os.Getenv("GROCERIES_LIVE_PDF")
	if key == "" || pdf == "" {
		t.Skip("GROCERIES_LIVE_GEMINI_KEY and GROCERIES_LIVE_PDF not set")
	}
	data, err := os.ReadFile(pdf)
	require.NoError(t, err)
	text, _ := os.ReadFile(os.Getenv("GROCERIES_LIVE_TEXT"))
	cfg, _ := json.Marshal(map[string]any{"gemini": Gemini{APIKey: key, Model: os.Getenv("GROCERIES_LIVE_MODEL"), Context: os.Getenv("GROCERIES_LIVE_CONTEXT")}})
	m, err := New(cfg)
	require.NoError(t, err)
	m.gemini.URL = "https://generativelanguage.googleapis.com/v1beta/interactions"
	o := Order{Email: "<live@x>", From: "Waitrose & Partners <no_reply@waitrose.co.uk>", Subject: "Order receipt",
		Received: time.Now(), Text: string(text), Files: []File{{Name: "receipt.pdf", Type: "application/pdf", Data: data}}}
	act, _ := json.Marshal(o)
	env := monitortest.Env(time.Now())
	env.HTTP = http.DefaultClient
	require.NoError(t, m.Act(context.Background(), env, monitor.Action{Type: OrderAction, Data: act}))
	start := time.Now()
	res, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	t.Logf("took %s", time.Since(start))
	var items []Item
	env.State.Get(itemsKey, &items)
	for _, it := range items {
		t.Logf("%-40s %-10s x%d keeps %d", it.Name, it.Storage, it.Qty, it.Keeps)
	}
	out, _ := json.MarshalIndent(res.Blocks, "", " ")
	t.Logf("%s", out)
}
