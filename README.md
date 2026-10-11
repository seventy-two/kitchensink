# kitchensink

A small self-hosted service that keeps an eye on things for you (stock
prices, NFL games, the weather, car adverts, your calendars) and tells your devices when
something happens. Clients get a live dashboard of everything being watched
and a notification for each event.

- **Server**: Go, one binary, SQLite. Runs the monitors on their intervals,
  keeps an event log and serves an HTTP API with a live
  [server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html)
  stream.
- **Windows dashboard**: native C++ (Win32, Dear ImGui), in `clients/windows`.
- Optional **ntfy** notifier, for phones without a kitchensink client.

## Monitors

| Type | Source | Notifies |
| --- | --- | --- |
| `stocks` | CNBC quotes, plus a week's change from CNBC's daily closes (fetched once a day); `holdings` (largest first) lead the table and make up the summary | a symbol's daily move passing each multiple of `movePct`; a price crossing a target |
| `nfl` | ESPN scoreboard | kick-off, scores and results for followed teams; optionally every result |
| `weather` | [Open-Meteo](https://open-meteo.com) (no key needed) | rain due within the hour, frost, strong gusts |
| `autotrader` | AutoTrader UK search | newly listed cars |
| `todo` | a to-do list kept on the server, changed from clients | nothing; ticked tasks clear after `clearAfter` (default `24h`) |
| `calendar` | iCalendar (ICS) feeds, e.g. Google or Outlook | each timed event, `remindMinutes` (default 10) before it starts |

Each monitor also returns a dashboard panel, polls on its own interval and
can ask to be polled sooner (the NFL monitor polls every minute while a
followed game is on).

### Calendars

The `calendar` monitor merges any number of ICS feeds into one view of the
next `days` days, one table per day, and links each event's Teams, Meet or
Zoom call. Recurring events, exceptions and moved instances are handled.
With `explain` set (a Gemini API key, and optionally a little `context`
about you), each event shown gets a subtitle from Gemini, found with Google
Search, listing what it is, when and where (e.g. "Eagles v Jaguars ·
14:30 kick-off · Tottenham Hotspur Stadium, London"), or none when
there's nothing to add. A note is made when an event first shows and
again on its day, for the latest details; if Gemini is unavailable the
calendar shows without them.

Each feed's address is a secret, so put it in the environment:

- **Google Calendar**: Settings → the calendar → *Integrate calendar* →
  *Secret address in iCal format*.
- **Outlook / Microsoft 365**: Settings → Calendar → *Shared calendars* →
  *Publish a calendar*, with *Can view all details*, then copy the ICS link.
  Some work accounts don't allow publishing.

## Running it

```sh
go build ./cmd/kitchensink
cp kitchensink.example.json kitchensink.json   # then edit it
KITCHENSINK_TOKEN=$(openssl rand -hex 32) ./kitchensink -config kitchensink.json
```

`-check` validates the config and exits. `deploy/` has an example systemd
unit and nginx location.

### Config

```json
{
  "listen": "127.0.0.1:6030",
  "database": "kitchensink.db",
  "token": "${KITCHENSINK_TOKEN}",
  "notifiers": [{ "type": "ntfy", "config": { "url": "https://ntfy.sh/your-topic" } }],
  "monitors": [
    { "type": "stocks", "interval": "5m", "config": { "symbols": ["AAPL", ".FTSE"], "movePct": 5 } }
  ]
}
```

- `${NAME}` is replaced with an environment variable, so tokens and
  personal details (postcodes, search URLs) can stay out of the file.
- Each monitor takes `id` (defaults to the type; needed only when you use a
  type twice), `title`, `interval` (default `5m`), `disabled` and its own
  `config`. Unknown fields are errors, so typos show up at startup.
- See `kitchensink.example.json` and each monitor's `Config` type for the
  options.

## API

Every `/api` request needs `Authorization: Bearer <token>`.

| Request | Returns |
| --- | --- |
| `GET /api/monitors` | `{"monitors": [Snapshot...]}`, in config order |
| `GET /api/events?since=ID&limit=N` | `{"events": [Event...]}` after `since`, or the latest `limit` (default 100) |
| `GET /api/stream?since=ID&heartbeat=S` | a server-sent events stream (below) |
| `POST /api/monitors/{id}/refresh` | 202; the monitor polls now |
| `POST /api/monitors/{id}/actions` | 204; applies a JSON action such as `{"type":"add","text":"Bins"}`, `{"type":"toggle","id":"…"}` or `{"type":"delete","id":"…"}`, then polls. 400 for an action the monitor refuses, 405 for a monitor that takes none |
| `GET /healthz` | `ok`, no token needed |

The stream sends:

1. `event` for each event after `since` (or the `Last-Event-ID` header),
   so a client that reconnects catches up on what it missed;
2. `snapshot` for every monitor;
3. `ready`, with `{"last": ID}`;
4. then `event` and `snapshot` messages as they happen, and a `: ping`
   comment every `heartbeat` seconds (default 30, 5 to 900).

Each `event` message carries the event's ID as its SSE `id`.

**Event**: `id`, `monitor`, `key`, `time`, `title`, `body`, `url`, `image`,
`priority` (`low`, `normal`, `high`) and `quiet`. Quiet events go in the
history but shouldn't raise a notification.

**Snapshot**: `monitor`, `kind`, `title`, `updated`, `error` (the last
poll's, with the last good blocks kept), `subtitle` (what it's watching, when that isn't obvious, e.g. a car
search's filters), `blocks` and `summary`: a few
stats (as in a `stats` block) worth seeing at a glance, which clients show
instead of the blocks when space is short, and `summaryStyle`: empty for
label/value/detail tiles, or `chips` for small blocks filled with each
stat's tone (the watchlist's daily moves). Every monitor is
drawn from the same few block types, so clients need no per-monitor code:

| Block `type` | Fields |
| --- | --- |
| `stats` | `stats`: `label`, `value`, `detail`, `tone`, `series` (a sparkline) |
| `table` | `columns` (`title`, `align`, `fill` for the column that takes spare width) and `rows` of cells (`text`, `tone`, `url`, and `detail` for a smaller line under the text) |
| `list` | `items`: `title`, `subtitle`, `detail`, `url`, `image`, `tone` |
| `checklist` | `items` (`id`, `title`, `done`) with checkboxes that send `toggle` and `delete` actions; `addHint` adds a box for `add` |
| `text` | `text` |

Tones are `good`, `bad`, `warn`, `muted`, `accent`, or empty for plain text.

## Writing a monitor

A monitor is a package that implements `monitor.Monitor` and registers a
factory:

```go
package mything

func init() {
	monitor.Register("mything", func(raw json.RawMessage) (monitor.Monitor, error) {
		var c Config
		if err := monitor.Decode(raw, &c); err != nil {
			return nil, err
		}
		return &Monitor{cfg: c}, nil
	})
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	// Fetch with env.HTTP; keep anything between polls in env.State.
	return monitor.Result{
		Blocks:  []monitor.Block{{Type: monitor.BlockText, Text: "hello"}},
		Summary: []monitor.Stat{{Label: "Status", Value: "fine"}},
		Events:  []monitor.Event{{Key: "something-2026-10-10", Title: "Something happened"}},
	}, nil
}
```

Then import it from `cmd/kitchensink/main.go` and add it to the config.

- A monitor that clients can change (like `todo`) also implements
  `monitor.Actor`. The engine never runs `Act` alongside `Poll`, and polls
  straight after, so changes show at once. Wrap `monitor.ErrBadAction` for
  actions it can't do.
- Event keys only need to be unique within the monitor. The engine drops
  any key it has seen before, so a monitor can report the current state on
  every poll without repeating notifications.
- `monitor/monitortest` has an in-memory `State`, an `Env` with a fixed
  clock and a server that replays saved API responses; the bundled
  monitors' tests show how to use them.

## Licence

MIT
