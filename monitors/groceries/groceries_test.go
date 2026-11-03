package groceries

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
	"github.com/seventy-two/kitchensink/monitor/monitortest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var london, _ = time.LoadLocation("Europe/London")

// Tuesday 3 November 2026, 09:00: the receipt's come, the food's due at 4.
var now = time.Date(2026, 11, 3, 9, 0, 0, 0, london)

// fakeGemini answers receipts with order and anything else with recipes,
// and records what it's asked.
type fakeGemini struct {
	*httptest.Server
	mu      sync.Mutex
	order   orderReply
	recipes []Recipe
	orders  []json.RawMessage // inputs of order requests
	asked   []string          // inputs of recipe requests
	fail    int               // status to fail with
}

func newFakeGemini(t *testing.T) *fakeGemini {
	g := &fakeGemini{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input  json.RawMessage `json:"input"`
			System string          `json:"system_instruction"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.fail != 0 {
			http.Error(w, "no", g.fail)
			return
		}
		var reply any
		if strings.Contains(req.System, "receipt") {
			g.orders = append(g.orders, req.Input)
			reply = g.order
		} else {
			var s string
			json.Unmarshal(req.Input, &s)
			g.asked = append(g.asked, s)
			reply = g.recipes
		}
		text, _ := json.Marshal(reply)
		json.NewEncoder(w).Encode(map[string]any{"steps": []any{
			map[string]any{"type": "thought"},
			map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": string(text)}}},
		}})
	}))
	t.Cleanup(g.Close)
	return g
}

func newMonitor(t *testing.T, g *fakeGemini) *Monitor {
	m, err := New(json.RawMessage(`{"gemini": {"apiKey": "k", "url": "` + g.URL + `", "context": "Two people."}}`))
	require.NoError(t, err)
	return m
}

func at(env monitor.Env, t time.Time) monitor.Env {
	env.Now = func() time.Time { return t }
	return env
}

func receipt(email string) monitor.Action {
	data, _ := json.Marshal(Order{
		Email: email, From: "Waitrose <no_reply@waitrose.co.uk>", Subject: "Order receipt: Tuesday 3 November",
		Received: now.Add(-time.Hour), Text: "Receipt attached 4pm - 8pm Order No. 1071209906",
		Files: []File{{Name: "Receipt.pdf", Type: "application/pdf", Data: []byte("%PDF")}, {Name: "x.png", Type: "image/png", Data: []byte("png")}},
	})
	return monitor.Action{Type: OrderAction, Data: data}
}

type item = struct {
	Name      string `json:"name"`
	Qty       int    `json:"qty"`
	Storage   string `json:"storage"`
	KeepsDays int    `json:"keepsDays"`
	Egg       bool   `json:"egg"`
}

func TestOrderReadOnceThenRecipes(t *testing.T) {
	g := newFakeGemini(t)
	g.order = orderReply{OrderRef: "1071209906", Delivery: "2026-11-03", Items: []item{
		{Name: "vegetable sushi", Qty: 1, Storage: "fridge", KeepsDays: 0},
		{Name: "Fresh basil", Qty: 4, Storage: "fridge", KeepsDays: 4},
		{Name: "Mushrooms", Qty: 1, Storage: "fridge", KeepsDays: 3},
		{Name: "Soured cream", Qty: 1, Storage: "fridge", KeepsDays: 7},
		{Name: "Linguine", Qty: 1, Storage: "cupboard", KeepsDays: 365},
	}}
	g.recipes = []Recipe{{Title: "Mushroom linguine", Uses: []string{"Mushrooms", "Fresh basil"}, Minutes: 20, Method: "Fry the mushrooms."},
		{Title: "Pesto", Uses: []string{"Fresh basil"}, Buy: []string{"Pine nuts"}, Method: "Blend."}}
	m := newMonitor(t, g)
	env := monitortest.Env(now)
	ctx := context.Background()

	require.NoError(t, m.Act(ctx, env, receipt("<r1@x>")))
	require.NoError(t, m.Act(ctx, env, receipt("<r1@x>")), "the same email again is fine")
	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	require.Len(t, g.orders, 1)
	var in []map[string]any
	require.NoError(t, json.Unmarshal(g.orders[0], &in))
	require.Len(t, in, 2, "the PDF only, then the email")
	assert.Equal(t, map[string]any{"type": "document", "mime_type": "application/pdf", "data": "JVBERg=="}, in[0])
	assert.Contains(t, in[1]["text"], "Order No. 1071209906")

	// Recipes for what goes off within three days, with the rest offered.
	require.Len(t, g.asked, 1)
	assert.Contains(t, g.asked[0], "Today is Tuesday 3 November")
	assert.Contains(t, g.asked[0], "Going off soon:\n- Vegetable sushi (fridge): Today\n- Mushrooms (fridge): Friday\n")
	assert.Contains(t, g.asked[0], "Also in:\n- Fresh basil (fridge): Saturday\n- Soured cream (fridge): 10 Nov\n\nLong-lasting things bought in recent months:\n- Linguine (cupboard)\n")
	assert.Contains(t, g.asked[0], "Suggest 3 recipes.")

	// The panel lists what goes off within three weeks, soonest first.
	require.Len(t, res.Blocks, 3)
	rows := res.Blocks[0].Rows
	require.Len(t, rows, 4)
	assert.Equal(t, monitor.Cell{Text: "Vegetable sushi", Detail: "Fridge"}, rows[0][0])
	assert.Equal(t, monitor.Cell{Text: "Today", Tone: monitor.ToneBad}, rows[0][1])
	assert.Equal(t, &monitor.CellAction{Type: UsedAction, ID: "1071209906-vegetable-sushi", Label: "Used up or thrown away"}, rows[0][2].Action)
	assert.Equal(t, monitor.Cell{Text: "Friday", Tone: monitor.ToneWarn}, rows[1][1])
	assert.Equal(t, "Fresh basil ×4", rows[2][0].Text)
	assert.Equal(t, monitor.Cell{Text: "10 Nov"}, rows[3][1], "a week on: not soon")
	assert.Equal(t, "And 1 longer-lasting thing from the shopping.", res.Blocks[1].Text)
	assert.Equal(t, monitor.Item{Title: "Mushroom linguine (20 min)", Subtitle: "Uses mushrooms, fresh basil", Detail: "Fry the mushrooms."}, res.Blocks[2].Items[0])
	assert.Equal(t, "Uses fresh basil · buy pine nuts", res.Blocks[2].Items[1].Subtitle)
	assert.Equal(t, []monitor.Stat{{Label: "Use by Today", Value: "Vegetable sushi", Detail: "+1 more by Friday", Tone: monitor.ToneBad}}, res.Summary)
	require.NotNil(t, res.SummaryBlock)
	assert.Len(t, res.SummaryBlock.Rows, 2, "the short view is what's going off soon")
	assert.Equal(t, 15*time.Hour+time.Minute, res.Next, "just after midnight")

	// Polled again: nothing asked.
	_, err = m.Poll(ctx, at(env, now.Add(time.Hour)))
	require.NoError(t, err)
	assert.Len(t, g.asked, 1)

	// Another email about the same order isn't read.
	require.NoError(t, m.Act(ctx, env, receipt("<r2@x>")))
	_, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Len(t, g.orders, 1)

	// Marking the sushi eaten asks for new recipes, once it's settled.
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: UsedAction, ID: "1071209906-vegetable-sushi"}))
	assert.ErrorIs(t, m.Act(ctx, env, monitor.Action{Type: UsedAction, ID: "1071209906-vegetable-sushi"}), monitor.ErrBadAction)
	res, err = m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Len(t, g.asked, 1)
	assert.Equal(t, settle, res.Next)
	_, err = m.Poll(ctx, at(env, now.Add(settle)))
	require.NoError(t, err)
	require.Len(t, g.asked, 2)
	assert.NotContains(t, g.asked[1], "sushi")

	// The next morning things are a day nearer: recipes again, once.
	tomorrow := at(env, now.Add(24*time.Hour))
	res, err = m.Poll(ctx, tomorrow)
	require.NoError(t, err)
	require.Len(t, g.asked, 3)
	assert.Contains(t, g.asked[2], "- Mushrooms (fridge): Friday\n- Fresh basil (fridge): Saturday\n")
	_, err = m.Poll(ctx, tomorrow)
	require.NoError(t, err)
	assert.Len(t, g.asked, 3)

	// Things gone off stay a day, then go; everything goes after four weeks.
	res, err = m.Poll(ctx, at(env, now.AddDate(0, 0, 4)))
	require.NoError(t, err)
	assert.Equal(t, monitor.Cell{Text: "Gone off", Tone: monitor.ToneBad}, res.Blocks[0].Rows[0][1])
	res, err = m.Poll(ctx, at(env, now.AddDate(0, 0, 5)))
	require.NoError(t, err)
	assert.Equal(t, "Fresh basil ×4", res.Blocks[0].Rows[0][0].Text)
	res, err = m.Poll(ctx, at(env, now.AddDate(0, 0, 30)))
	require.NoError(t, err)
	assert.Equal(t, "Nothing from the shopping that goes off soon.", res.Blocks[0].Text)
	assert.Nil(t, res.SummaryBlock)
	assert.Equal(t, "Nothing going off", res.Summary[0].Value)
}

func TestGeminiFailing(t *testing.T) {
	g := newFakeGemini(t)
	g.fail = http.StatusTooManyRequests
	m := newMonitor(t, g)
	env := monitortest.Env(now)
	ctx := context.Background()
	require.NoError(t, m.Act(ctx, env, receipt("<r1@x>")))
	for range maxAttempts + 1 {
		res, err := m.Poll(ctx, env)
		require.NoError(t, err)
		assert.Equal(t, retryPoll, res.Next)
		assert.Contains(t, res.Blocks[0].Text, "Couldn't read the order")
	}
	// Out of credit isn't the receipt's fault: it waits.
	g.fail = 0
	g.order = orderReply{OrderRef: "1", Items: []item{{Name: "Tofu", Qty: 2, Storage: "fridge", KeepsDays: 14}}}
	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	assert.Equal(t, "Tofu ×2", res.Blocks[0].Rows[0][0].Text)
	assert.Empty(t, g.asked, "nothing going off soon: no recipes")

	// A broken receipt is given up on after a few tries.
	g.fail = http.StatusInternalServerError
	data, _ := json.Marshal(Order{Email: "<r2@x>", Received: now, Text: "Order No. 222222"})
	require.NoError(t, m.Act(ctx, env, monitor.Action{Type: OrderAction, Data: data}))
	for range maxAttempts {
		_, err = m.Poll(ctx, env)
		require.NoError(t, err)
	}
	var pending []Order
	_, err = env.State.Get(pendingKey, &pending)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

// Past orders are read however old, for what's usually in: staples stay
// until their use-by, perishables are forgotten, and egg is left out of
// recipes.
func TestPastOrdersAndEgg(t *testing.T) {
	g := newFakeGemini(t)
	m := newMonitor(t, g)
	ctx := context.Background()
	env := monitortest.Env(now)
	past := func(email, ref string, received time.Time) monitor.Action {
		data, _ := json.Marshal(Order{Email: email, Received: received, Text: "Order No. " + ref, Past: true})
		return monitor.Action{Type: OrderAction, Data: data}
	}
	g.order = orderReply{OrderRef: "111111", Delivery: "2026-06-01", Items: []item{
		{Name: "Plain flour", Qty: 1, Storage: "cupboard", KeepsDays: 300},
		{Name: "Basmati rice", Qty: 1, Storage: "cupboard", KeepsDays: 900},
		{Name: "Egg tagliatelle", Qty: 1, Storage: "cupboard", KeepsDays: 300, Egg: true},
		{Name: "Cheddar", Qty: 1, Storage: "fridge", KeepsDays: 30},
		{Name: "Vegetarian burgers", Qty: 1, Storage: "freezer", KeepsDays: 200, Egg: true},
		{Name: "Noodles", Qty: 1, Storage: "cupboard", KeepsDays: 155},
	}}
	require.NoError(t, m.Act(ctx, env, past("<p1@x>", "111111", time.Date(2026, 6, 1, 8, 0, 0, 0, london))))
	_, err := m.Poll(ctx, env)
	require.NoError(t, err)
	g.order = orderReply{OrderRef: "222222", Delivery: "2026-11-03", Items: []item{
		{Name: "Plain flour", Qty: 1, Storage: "cupboard", KeepsDays: 300},
		{Name: "Croissants", Qty: 1, Storage: "cupboard", KeepsDays: 2, Egg: true},
		{Name: "Mushrooms", Qty: 1, Storage: "fridge", KeepsDays: 3},
		{Name: "Frozen vegetarian burgers", Qty: 1, Storage: "freezer", KeepsDays: 200},
		{Name: "Cheddar", Qty: 1, Storage: "fridge", KeepsDays: 30},
	}}
	require.NoError(t, m.Act(ctx, env, receipt("<r1@x>")))
	res, err := m.Poll(ctx, env)
	require.NoError(t, err)
	require.Len(t, g.orders, 2)

	var items []Item
	_, err = env.State.Get(itemsKey, &items)
	require.NoError(t, err)
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	assert.ElementsMatch(t, []string{"Croissants", "Mushrooms", "Plain flour", "Plain flour", "Egg tagliatelle", "Basmati rice",
		"Vegetarian burgers", "Frozen vegetarian burgers", "Noodles", "Cheddar"}, names,
		"June's cheddar is long gone; rice is kept a year at most, from June")

	require.Len(t, g.asked, 1)
	assert.Equal(t, "Today is Tuesday 3 November.\n\nGoing off soon:\n- Mushrooms (fridge): Friday\n\nAlso in:\n- Cheddar (fridge)\n\n"+
		"Long-lasting things bought in recent months:\n- Basmati rice (cupboard)\n- Noodles (cupboard)\n- Plain flour (cupboard)\n\nSuggest 3 recipes.",
		g.asked[0], "the burgers have egg, even where Gemini didn't say so; June's noodles, at their date, aren't going off but assumed")
	assert.Equal(t, monitor.Cell{Text: "Croissants", Detail: "Cupboard · has egg"}, res.Blocks[0].Rows[0][0])
	assert.Len(t, res.Blocks[0].Rows, 2, "croissants and mushrooms; staples aren't listed")
	assert.Equal(t, "And 7 longer-lasting things from the shopping.", res.Blocks[1].Text)
}

func TestOldReceiptIgnored(t *testing.T) {
	g := newFakeGemini(t)
	m := newMonitor(t, g)
	env := monitortest.Env(now.AddDate(0, 0, 20))
	require.NoError(t, m.Act(context.Background(), env, receipt("<r1@x>")))
	_, err := m.Poll(context.Background(), env)
	require.NoError(t, err)
	assert.Empty(t, g.orders)
}

func TestOrderRef(t *testing.T) {
	assert.Equal(t, "1071209906", orderRef("Receipt attached 4pm - 8pm Order No. 1071209906"))
	assert.Equal(t, "1071209906", orderRef("Order number:\n1071209906"))
	assert.Equal(t, "ab-12345", orderRef("Your order #AB-12345 is here"))
	assert.Equal(t, "", orderRef("Order summary\nName"))
}

func TestConfig(t *testing.T) {
	_, err := New(json.RawMessage(`{}`))
	assert.ErrorContains(t, err, "apiKey")
	_, err = New(json.RawMessage(`{"gemini": {"apiKey": "k"}, "soonDays": -1}`))
	assert.Error(t, err)
}
