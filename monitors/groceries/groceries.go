// Package groceries keeps track of the food from grocery deliveries and
// when it goes off, and suggests recipes for what's going off soon.
//
// The mail monitor hands it each delivery's receipt (an "order" action
// with the email's text and attached PDFs). Gemini reads each order once,
// listing the food in it with how many days each thing keeps, and is
// asked again only for recipes, when what's going off soon changes.
package groceries

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("groceries", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	Gemini Gemini `json:"gemini"`
	// Timezone defaults to Europe/London.
	Timezone string `json:"timezone"`
	// SoonDays is how many days ahead counts as going off soon, for the
	// recipes and the panel's warnings; it defaults to 3.
	SoonDays int `json:"soonDays"`
	// ShowDays is the longest an item can keep and still be listed; longer
	// lasting food (pasta, frozen pizza) is only offered to recipes. It
	// defaults to 21.
	ShowDays int `json:"showDays"`
	// ForgetDays is how long after delivery everything from an order is
	// taken to be eaten; it defaults to 28.
	ForgetDays int `json:"forgetDays"`
	// GoneDays is how long something stays listed after going off, in
	// case it's still in the fridge; it defaults to 1.
	GoneDays *int `json:"goneDays"`
	// Recipes is how many recipes to suggest; it defaults to 3.
	Recipes int `json:"recipes"`
}

// Order is a delivery's receipt, as the mail monitor passes it on in an
// "order" action.
type Order struct {
	// Email is the email's Message-ID, so a receipt read twice is
	// known.
	Email    string    `json:"email"`
	From     string    `json:"from"`
	Subject  string    `json:"subject"`
	Received time.Time `json:"received"`
	Text     string    `json:"text"`
	Files    []File    `json:"files,omitempty"`
	// Past marks an old receipt read to learn what's usually kept in
	// (flour, rice, spices): it's read however old it is.
	Past bool `json:"past,omitempty"`
}

// File is an email's attachment, such as the full receipt as a PDF.
type File struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Data []byte `json:"data"`
}

// OrderAction is the action type an Order arrives as.
const OrderAction = "order"

// UsedAction marks an item, by ID, as eaten or thrown away.
const UsedAction = "used"

// Monitor is the groceries tracker.
type Monitor struct {
	gemini                            *gemini
	loc                               *time.Location
	soon, show, forget, gone, recipes int
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Timezone: "Europe/London", SoonDays: 3, ShowDays: 21, ForgetDays: 28, Recipes: 3}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return nil, fmt.Errorf("timezone: %w", err)
	}
	g, err := newGemini(c.Gemini)
	if err != nil {
		return nil, err
	}
	gone := 1
	if c.GoneDays != nil {
		gone = *c.GoneDays
	}
	if c.SoonDays < 0 || c.ShowDays < 1 || c.ForgetDays < 1 || gone < 0 || c.Recipes < 0 {
		return nil, errors.New("soonDays, showDays, forgetDays, goneDays and recipes can't be negative")
	}
	return &Monitor{gemini: g, loc: loc, soon: c.SoonDays, show: c.ShowDays, forget: c.ForgetDays, gone: gone, recipes: c.Recipes}, nil
}

// State keys.
const (
	pendingKey = "pending" // receipts waiting to be read
	ordersKey  = "orders"  // orders read, by reference
	itemsKey   = "items"   // the food, from every order still remembered
	recipesKey = "recipes" // the latest suggestions, and what they were for
	failedKey  = "failed"  // Gemini failures, by email
	changedKey = "changed" // when an item was last marked used
)

// Item is a thing bought, from one order.
type Item struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Qty     int    `json:"qty,omitempty"`
	Storage string `json:"storage,omitempty"`
	Order   string `json:"order"`
	// Delivered is the day it came, and Keeps how many days from then
	// it's good for.
	Delivered string `json:"delivered"`
	Keeps     int    `json:"keeps"`
	// Egg is set for food with egg in it, which recipes leave out.
	Egg bool `json:"egg,omitempty"`
}

// staple is something that keeps for months (flour, rice, tins, frozen
// food): it's taken to be in until its use-by, however long ago it came,
// rather than eaten after forgetDays.
func (it Item) staple() bool { return it.Keeps >= stapleDays }

// useBy is the last day it's good.
func (it Item) useBy(loc *time.Location) time.Time {
	d, err := time.ParseInLocation(time.DateOnly, it.Delivered, loc)
	if err != nil {
		return time.Time{}
	}
	return d.AddDate(0, 0, it.Keeps)
}

// orderRead is an order that's been read, so it's never read again.
type orderRead struct {
	Email     string    `json:"email"`
	Delivered string    `json:"delivered"`
	Read      time.Time `json:"read"`
	Items     int       `json:"items"`
}

// recipes are the suggestions for one set of things going off.
type recipes struct {
	For  string    `json:"for"` // soonKey of what they were asked for
	Made time.Time `json:"made"`
	List []Recipe  `json:"list"`
}

// Limits.
const (
	maxAttempts = 4               // Gemini failures before a receipt is dropped
	retryPoll   = 2 * time.Minute // after Gemini fails
	settle      = 2 * time.Minute // after marking things used, before asking for new recipes
	maxFiles    = 10 << 20        // bytes of attachments kept for one receipt
	oldReceipt  = 14 * 24 * time.Hour
	readBudget  = 50 * time.Second // no new order is started after this, so a poll fits in two minutes
	stapleDays  = 60
	maxKeeps    = 365
)

// Act takes a receipt from the mail monitor, or marks an item used.
func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	switch a.Type {
	case OrderAction:
		var o Order
		if err := json.Unmarshal(a.Data, &o); err != nil {
			return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
		}
		if o.Email == "" || strings.TrimSpace(o.Text) == "" && len(o.Files) == 0 {
			return fmt.Errorf("%w: an order needs its email and some text or a file", monitor.ErrBadAction)
		}
		var pending []Order
		if _, err := env.State.Get(pendingKey, &pending); err != nil {
			return err
		}
		orders := map[string]orderRead{}
		if _, err := env.State.Get(ordersKey, &orders); err != nil {
			return err
		}
		if slices.ContainsFunc(pending, func(p Order) bool { return p.Email == o.Email }) ||
			slices.ContainsFunc(slices.Collect(maps.Values(orders)), func(r orderRead) bool { return r.Email == o.Email }) {
			return nil // read already, or about to be
		}
		size := 0
		o.Files = slices.DeleteFunc(o.Files, func(f File) bool {
			size += len(f.Data)
			return size > maxFiles || f.Type != "application/pdf"
		})
		return env.State.Set(pendingKey, append(pending, o))
	case UsedAction:
		var items []Item
		if _, err := env.State.Get(itemsKey, &items); err != nil {
			return err
		}
		n := len(items)
		items = slices.DeleteFunc(items, func(it Item) bool { return it.ID == a.ID })
		if len(items) == n {
			return fmt.Errorf("%w: no item %q", monitor.ErrBadAction, a.ID)
		}
		if err := env.State.Set(changedKey, env.Now()); err != nil {
			return err
		}
		return env.State.Set(itemsKey, items)
	}
	return fmt.Errorf("%w: %s", monitor.ErrBadAction, a.Type)
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	now := env.Now()
	var (
		pending []Order
		items   []Item
		orders  = map[string]orderRead{}
		failed  = map[string]int{}
		rs      recipes
		changed time.Time
	)
	for k, v := range map[string]any{pendingKey: &pending, itemsKey: &items, ordersKey: &orders, failedKey: &failed, recipesKey: &rs, changedKey: &changed} {
		if _, err := env.State.Get(k, v); err != nil {
			return monitor.Result{}, fmt.Errorf("reading %s: %w", k, err)
		}
	}
	today := midnight(now.In(m.loc))
	var problem string
	next := time.Duration(0)
	sooner := func(d time.Duration) {
		if d > 0 && (next == 0 || d < next) {
			next = d
		}
	}

	// Read each new order once.
	started := time.Now()
	for len(pending) > 0 && ctx.Err() == nil && time.Since(started) < readBudget {
		o := pending[0]
		ref := orderRef(o.Subject + "\n" + o.Text)
		if _, done := orders[ref]; ref != "" && done || !o.Past && now.Sub(o.Received) > oldReceipt {
			pending = pending[1:] // a copy of one read already, or too old to matter
			continue
		}
		x, err := m.gemini.order(ctx, env.HTTP, m.orderInput(o))
		if err != nil {
			env.Log.Warn("reading a grocery order", "subject", o.Subject, "err", err)
			problem = "Couldn't read the order: " + err.Error()
			if failed[o.Email]++; failed[o.Email] >= maxAttempts && !errors.Is(err, errUnavailable) {
				delete(failed, o.Email)
				pending = pending[1:]
				continue
			}
			sooner(retryPoll)
			break
		}
		delete(failed, o.Email)
		pending = pending[1:]
		ref = cmp.Or(ref, slug(x.OrderRef), slug(o.Email))
		delivered := isoDate.FindString(x.Delivery)
		if _, err := time.Parse(time.DateOnly, delivered); err != nil {
			delivered = o.Received.In(m.loc).Format(time.DateOnly)
		}
		// A receipt read again (amended, say) replaces the order's items.
		items = slices.DeleteFunc(items, func(it Item) bool { return it.Order == ref })
		seen := map[string]int{}
		for _, xi := range x.Items {
			name := strings.TrimSpace(xi.Name)
			id := ref + "-" + slug(name)
			if name == "" || slug(name) == "" {
				continue
			}
			if seen[id]++; seen[id] > 1 {
				id += fmt.Sprint("-", seen[id])
			}
			items = append(items, Item{ID: id, Name: capitalise(name), Qty: max(xi.Qty, 1), Storage: xi.Storage,
				Order: ref, Delivered: delivered, Keeps: min(max(xi.KeepsDays, 0), maxKeeps), Egg: xi.Egg})
		}
		orders[ref] = orderRead{Email: o.Email, Delivered: delivered, Read: now, Items: len(x.Items)}
	}

	markEgg(items)

	// Forget what's long gone, or eaten by now.
	items = slices.DeleteFunc(items, func(it Item) bool {
		d, err := time.ParseInLocation(time.DateOnly, it.Delivered, m.loc)
		return err != nil || !it.staple() && today.Sub(d) > time.Duration(m.forget)*24*time.Hour ||
			today.Sub(it.useBy(m.loc)) > time.Duration(m.gone)*24*time.Hour
	})
	maps.DeleteFunc(orders, func(_ string, o orderRead) bool { return now.Sub(o.Read) > 400*24*time.Hour })
	slices.SortStableFunc(items, func(a, b Item) int {
		return cmp.Or(a.useBy(m.loc).Compare(b.useBy(m.loc)), strings.Compare(a.Name, b.Name))
	})

	// Recipes, when what's going off soon has changed (and isn't still
	// being changed by hand).
	soon := m.goingOff(items, today)
	key := soonKey(soon, today)
	switch {
	case len(soon) == 0 || m.recipes == 0:
		rs = recipes{}
	case rs.For != key && now.Sub(changed) < settle:
		sooner(settle - now.Sub(changed))
	case rs.For != key && ctx.Err() == nil:
		list, err := m.gemini.recipes(ctx, env.HTTP, m.recipesInput(soon, items, today), m.recipes)
		if err != nil {
			env.Log.Warn("asking for recipes", "err", err)
			problem = cmp.Or(problem, "Couldn't get recipes: "+err.Error())
			sooner(retryPoll)
		} else {
			rs = recipes{For: key, Made: now, List: list}
		}
	}

	for k, v := range map[string]any{pendingKey: pending, itemsKey: items, ordersKey: orders, failedKey: failed, recipesKey: rs} {
		if err := env.State.Set(k, v); err != nil {
			return monitor.Result{}, fmt.Errorf("saving %s: %w", k, err)
		}
	}
	if len(pending) > 0 && next == 0 {
		next = time.Second // cut short by the poll's deadline
	}
	// Look again just after midnight, when everything's a day nearer.
	sooner(today.AddDate(0, 0, 1).Add(time.Minute).Sub(now))
	res := m.result(items, rs, problem, len(pending), today)
	res.Next = next
	return res, nil
}

// goingOff is what goes off within soonDays (or has just gone).
func (m *Monitor) goingOff(items []Item, today time.Time) []Item {
	var out []Item
	limit := today.AddDate(0, 0, m.soon)
	for _, it := range items {
		if !it.staple() && !it.useBy(m.loc).After(limit) {
			out = append(out, it)
		}
	}
	return out
}

// markEgg marks as having egg everything named like something Gemini
// said had egg in another order ("Frozen vegetarian mozzarella burgers"
// after "Vegetarian mozzarella burgers"), as it doesn't always say the
// same of one product.
func markEgg(items []Item) {
	var egg []string
	for _, it := range items {
		if it.Egg {
			egg = append(egg, plainName(it.Name))
		}
	}
	for i, it := range items {
		n := plainName(it.Name)
		items[i].Egg = it.Egg || slices.ContainsFunc(egg, func(e string) bool { return strings.Contains(n, e) })
	}
}

// plainName is a name to compare: lower case, without words that only
// say how it's sold.
func plainName(s string) string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(s)) {
		switch w {
		case "frozen", "fresh", "chilled":
		default:
			out = append(out, w)
		}
	}
	return " " + strings.Join(out, " ") + " "
}

// soonKey says what recipes were asked for: the things going off, each
// with how many days it has left (so the suggestions follow the days
// round, but no more than once a day).
func soonKey(soon []Item, today time.Time) string {
	var parts []string
	for _, it := range soon {
		parts = append(parts, it.ID)
	}
	slices.Sort(parts)
	return today.Format(time.DateOnly) + ":" + strings.Join(parts, ",")
}

// orderRef finds an order number in a receipt ("Order No. 1071209906",
// "Order number: 1071209906"), so the same order is never read twice.
var orderNumber = regexp.MustCompile(`(?i)\border\s*(?:no\.?|number|num|#|ref(?:erence)?)\s*:?\s*([A-Z0-9][A-Z0-9-]{4,})`)

func orderRef(s string) string {
	if mm := orderNumber.FindStringSubmatch(s); mm != nil {
		return slug(mm[1])
	}
	return ""
}

var isoDate = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	return b.String()
}

func capitalise(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func midnight(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
}
