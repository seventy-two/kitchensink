// Package battery tells the user when a wireless device on their PC has
// finished charging, e.g. a Razer mouse. The Windows client reads the
// devices' batteries (with Squeak) and sends them as "battery" actions;
// the monitor raises an event the first time a charging device reaches
// full. It's a background monitor: clients show its readings only under
// diagnostics.
package battery

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("battery", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// Full is the percentage that counts as fully charged; 100 by default.
	Full int `json:"full"`
}

// Monitor is the battery watcher.
type Monitor struct {
	full int
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{Full: 100}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.Full < 1 || c.Full > 100 {
		return nil, fmt.Errorf("full must be 1-100")
	}
	return &Monitor{full: c.Full}, nil
}

// Reading is one device as the client reports it.
type Reading struct {
	Name     string `json:"name"`
	Battery  int    `json:"battery"` // percent; negative if unknown
	Charging bool   `json:"charging"`
}

// device is what's kept about each device, by name.
type device struct {
	Reading
	Seen time.Time `json:"seen"`
	// Since is when the current charge began; zero while not charging.
	Since time.Time `json:"since,omitzero"`
	// Told is whether this charge's "fully charged" event has been raised.
	Told bool `json:"told,omitempty"`
}

// event is a raised event, kept so polls can return it (the engine drops
// repeats by key).
type event struct {
	Key  string    `json:"key"`
	Name string    `json:"name"`
	Time time.Time `json:"time"`
}

var london = func() *time.Location {
	if l, err := time.LoadLocation("Europe/London"); err == nil {
		return l
	}
	return time.UTC
}()

const (
	devicesKey = "devices"
	eventsKey  = "events"
	maxEvents  = 10
)

// Merge folds readings that share a name into one, as a wireless mouse can
// show up twice while it charges by cable (once on the cable, once through
// its dongle): charging if either says so, with the higher battery.
func Merge(rs []Reading) []Reading {
	at := map[string]int{}
	var out []Reading
	for _, r := range rs {
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" || r.Battery < 0 || r.Battery > 100 {
			continue
		}
		if i, ok := at[r.Name]; ok {
			out[i].Charging = out[i].Charging || r.Charging
			out[i].Battery = max(out[i].Battery, r.Battery)
			continue
		}
		at[r.Name] = len(out)
		out = append(out, r)
	}
	return out
}

func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	if a.Type != "battery" {
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	var body struct {
		Devices []Reading `json:"devices"`
	}
	if err := json.Unmarshal(a.Data, &body); err != nil {
		return fmt.Errorf("%w: %v", monitor.ErrBadAction, err)
	}
	if len(body.Devices) > 20 {
		return fmt.Errorf("%w: too many devices", monitor.ErrBadAction)
	}
	devices := map[string]device{}
	if _, err := env.State.Get(devicesKey, &devices); err != nil {
		return err
	}
	var events []event
	if _, err := env.State.Get(eventsKey, &events); err != nil {
		return err
	}
	now := env.Now()
	for _, r := range Merge(body.Devices) {
		d := devices[r.Name]
		switch {
		case !r.Charging:
			d.Since, d.Told = time.Time{}, false
		case d.Since.IsZero():
			d.Since, d.Told = now, false
		}
		if r.Charging && !d.Told && r.Battery >= m.full {
			d.Told = true
			events = append(events, event{Key: r.Name + "@" + strconv.FormatInt(d.Since.Unix(), 10), Name: r.Name, Time: now})
		}
		d.Reading, d.Seen = r, now
		devices[r.Name] = d
	}
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	if err := env.State.Set(devicesKey, devices); err != nil {
		return err
	}
	return env.State.Set(eventsKey, events)
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	devices := map[string]device{}
	if _, err := env.State.Get(devicesKey, &devices); err != nil {
		return monitor.Result{}, err
	}
	var events []event
	if _, err := env.State.Get(eventsKey, &events); err != nil {
		return monitor.Result{}, err
	}
	names := make([]string, 0, len(devices))
	for n := range devices {
		names = append(names, n)
	}
	sort.Strings(names)
	res := monitor.Result{Background: true}
	if len(names) == 0 {
		res.Blocks = []monitor.Block{{Type: monitor.BlockText, Text: "No readings yet: the Windows client sends them when Squeak is installed."}}
	} else {
		b := monitor.Block{Type: monitor.BlockTable, Columns: []monitor.Column{{Title: "Device"}, {Title: "Battery"}, {Title: "Read"}}}
		for _, n := range names {
			d := devices[n]
			level := monitor.Cell{Text: strconv.Itoa(d.Battery) + "%"}
			if d.Charging {
				level.Detail = "charging"
				level.Tone = monitor.ToneAccent
			} else if d.Battery <= 20 {
				level.Tone = monitor.ToneWarn
			}
			b.Rows = append(b.Rows, []monitor.Cell{{Text: n}, level, {Text: d.Seen.In(london).Format("Mon 15:04")}})
		}
		res.Blocks = []monitor.Block{b}
	}
	for _, e := range events {
		res.Events = append(res.Events, monitor.Event{
			Key:      e.Key,
			Time:     e.Time,
			Title:    e.Name + " is fully charged",
			Body:     "You can unplug it.",
			Priority: monitor.PriorityNormal,
			Until:    e.Time.Add(6 * time.Hour),
		})
	}
	return res, nil
}
