// Package todo is a to-do list kept by the server and changed from clients:
// add a task, tick it off, or delete it. Ticked tasks clear themselves
// after a day.
package todo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("todo", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

// Config is the monitor's config.
type Config struct {
	// ClearAfter is how long a ticked task stays before it's cleared. It
	// defaults to a day.
	ClearAfter monitor.Duration `json:"clearAfter"`
}

// Monitor is the to-do list.
type Monitor struct {
	clearAfter time.Duration
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	c := Config{ClearAfter: monitor.Duration(24 * time.Hour)}
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.ClearAfter <= 0 {
		return nil, fmt.Errorf("clearAfter must be positive")
	}
	return &Monitor{clearAfter: time.Duration(c.ClearAfter)}, nil
}

// task is one item on the list.
type task struct {
	ID    string     `json:"id"`
	Text  string     `json:"text"`
	Added time.Time  `json:"added"`
	Done  *time.Time `json:"done,omitempty"` // when it was ticked
}

// itemsKey is the state key holding the list, in the order tasks were
// added.
const itemsKey = "items"

// maxText bounds a task's length, in characters.
const maxText = 500

func load(env monitor.Env) ([]task, error) {
	var tasks []task
	if _, err := env.State.Get(itemsKey, &tasks); err != nil {
		return nil, fmt.Errorf("reading tasks: %w", err)
	}
	return tasks, nil
}

func (m *Monitor) Act(ctx context.Context, env monitor.Env, a monitor.Action) error {
	tasks, err := load(env)
	if err != nil {
		return err
	}
	find := func() (int, error) {
		for i, t := range tasks {
			if t.ID == a.ID {
				return i, nil
			}
		}
		return -1, fmt.Errorf("%w: no task %q", monitor.ErrBadAction, a.ID)
	}
	switch a.Type {
	case "add":
		text := strings.Join(strings.Fields(a.Text), " ")
		if text == "" {
			return fmt.Errorf("%w: a task needs some text", monitor.ErrBadAction)
		}
		if utf8.RuneCountInString(text) > maxText {
			return fmt.Errorf("%w: tasks are limited to %d characters", monitor.ErrBadAction, maxText)
		}
		tasks = append(tasks, task{ID: newID(), Text: text, Added: env.Now()})
	case "toggle":
		i, err := find()
		if err != nil {
			return err
		}
		if tasks[i].Done == nil {
			now := env.Now()
			tasks[i].Done = &now
		} else {
			tasks[i].Done = nil
		}
	case "delete":
		i, err := find()
		if err != nil {
			return err
		}
		tasks = append(tasks[:i], tasks[i+1:]...)
	case "move":
		// Swaps an open task with the open one above or below it, as
		// they're shown. Past either end, nothing happens.
		i, err := find()
		if err != nil {
			return err
		}
		if tasks[i].Done != nil {
			return fmt.Errorf("%w: ticked tasks stay in the order they were ticked", monitor.ErrBadAction)
		}
		step := map[string]int{"up": -1, "down": 1}[a.Text]
		if step == 0 {
			return fmt.Errorf("%w: move up or down, not %q", monitor.ErrBadAction, a.Text)
		}
		for j := i + step; j >= 0 && j < len(tasks); j += step {
			if tasks[j].Done == nil {
				tasks[i], tasks[j] = tasks[j], tasks[i]
				break
			}
		}
	default:
		return fmt.Errorf("%w: unknown action %q", monitor.ErrBadAction, a.Type)
	}
	return env.State.Set(itemsKey, tasks)
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	tasks, err := load(env)
	if err != nil {
		return monitor.Result{}, err
	}
	now := env.Now()

	// Clear tasks ticked over ClearAfter ago, and work out when the next
	// one is due to go.
	kept := tasks[:0:0]
	var next time.Duration
	for _, t := range tasks {
		if t.Done != nil {
			left := t.Done.Add(m.clearAfter).Sub(now)
			if left <= 0 {
				continue
			}
			if next == 0 || left < next {
				next = left
			}
		}
		kept = append(kept, t)
	}
	if len(kept) != len(tasks) {
		if err := env.State.Set(itemsKey, kept); err != nil {
			return monitor.Result{}, err
		}
	}

	// Open tasks first, in their order (oldest first unless moved); then
	// ticked ones, latest first.
	var open, done []task
	for _, t := range kept {
		if t.Done == nil {
			open = append(open, t)
		} else {
			done = append(done, t)
		}
	}
	sort.SliceStable(done, func(i, j int) bool { return done[i].Done.After(*done[j].Done) })
	b := monitor.Block{Type: monitor.BlockChecklist, Items: []monitor.Item{}, AddHint: "Add a task…", Movable: true}
	for _, t := range append(open, done...) {
		b.Items = append(b.Items, monitor.Item{ID: t.ID, Title: t.Text, Done: t.Done != nil})
	}
	// Poll a little after the next task is due to clear.
	if next > 0 {
		next += time.Second
	}
	return monitor.Result{Blocks: []monitor.Block{b}, Summary: summary(open), Next: next}, nil
}

// summary names the oldest open task.
func summary(open []task) []monitor.Stat {
	if len(open) == 0 {
		return []monitor.Stat{{Label: "To do", Value: "All done", Tone: monitor.ToneGood}}
	}
	return []monitor.Stat{{Label: "To do", Value: open[0].Text}}
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
