package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestLiveGemini sends made-up emails to the real Gemini API, to check the
// instruction and schema; it runs only with MAIL_LIVE_GEMINI_KEY set.
func TestLiveGemini(t *testing.T) {
	key := os.Getenv("MAIL_LIVE_GEMINI_KEY")
	if key == "" {
		t.Skip("MAIL_LIVE_GEMINI_KEY not set")
	}
	g, err := newGemini(Gemini{APIKey: key})
	require.NoError(t, err)
	m := &Monitor{gemini: g, loc: london, chores: "chores",
		choresDone: map[string]string{"waitrose-order": "a Waitrose groceries order being placed"}}
	for _, e := range []struct{ from, subject, body string }{
		{"Waitrose & Partners <customersupport@waitrose.co.uk>", "Your order is confirmed",
			"Thanks for your order 1234567. Delivery slot: Thursday 15 October, 10:00 - 11:00, to 1 Example Road. You can amend your order until 23:45 on Tuesday 13 October. Total £84.20"},
		{"Amazon.co.uk <auto-confirm@amazon.co.uk>", "Ordered: \"Finish Ultimate Plus All in 1...\" and 1 more item",
			"Order # 205-1234567-7654321. Arriving Thursday. Finish Ultimate Plus All in 1 Dishwasher Tablets, 100 Tablets £19.99. Anker USB-C Charger 65W £35.99"},
		{"Ticketmaster UK <customer_support@email.ticketmaster.co.uk>", "You got the tickets!",
			"Order 31-12345/UK. Humanity's Last Breath & Vildhjarta. O2 Academy Bristol. Sat 7 Nov 2026, doors 18:30. 2 x Standing. Mobile tickets in the app."},
		{"Origin Coffee <info@origincoffee.co.uk>", "Order confirmation #955625",
			"Thanks for your subscription order. 2 x Los Altos Filter 250g, ground for cafetiere. Your subscription renews every 3 weeks; next order 21 July."},
		{"Origin Coffee <info@origincoffee.co.uk>", "Your order has been dispatched", "Order #955625 is on its way with Royal Mail."},
		{"Boots <hello@boots.com>", "Your order could be 20% off this weekend",
			"Order now and save 20% on beauty. Free delivery over £25. Shop now."},
		{"Amazon.co.uk <return@amazon.co.uk>", "Your return label",
			"Return the Dr. Martens 1460 boots, order 205-111. Drop off at a Post Office by 30 October 2026 for your refund."},
		{"Bristol Dental <bookings@bristoldental.co.uk>", "Appointment cancelled",
			"Your appointment with Dr Smith on Tuesday 20 October at 09:30 (ref BD-4411) has been cancelled. Please call to rebook."},
	} {
		h := header{From: e.from, Subject: e.subject, Date: now.Add(-2 * time.Hour)}
		x, err := g.extract(context.Background(), http.DefaultClient, m.prompt(h, e.body, now))
		require.NoError(t, err, e.subject)
		out, _ := json.MarshalIndent(x, "", " ")
		t.Logf("%s\n%s", e.subject, out)
	}
}
