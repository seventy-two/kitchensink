# kitchensink

A small self-hosted service that keeps an eye on things for you (stock
prices, NFL games, your fantasy football teams, the weather, car adverts,
your calendars, a to-do list, recurring chores, the orders and
bookings in your email, the daily puzzle games you haven't played yet,
a day's progress towards OSRS pets, your Letterboxd watchlist and new
eBay listings) and tells your devices when something happens. Clients get a live dashboard of everything being watched and a
notification for each event.

- **Server**: Go, one binary, SQLite. Runs the monitors on their intervals,
  keeps an event log and serves an HTTP API with a live
  [server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html)
  stream.
- **Windows dashboard**: native C++ (Win32, Dear ImGui), in
  [kitchensink-windows](https://github.com/seventy-two/kitchensink-windows).
- **Android app** for phones, tablets and Android TV: Kotlin and Jetpack
  Compose, in [kitchensink-android](https://github.com/seventy-two/kitchensink-android).
- Optional **ntfy** notifier, for phones without a kitchensink client.

## Monitors

| Type | Source | Notifies |
| --- | --- | --- |
| `stocks` | CNBC quotes, plus a week's change from CNBC's daily closes (fetched once a day); `holdings` (largest first) lead the table and the summary, which has every symbol in the table's order (without holdings, the biggest movers first) | a holding's daily move (any symbol's, without `holdings`) passing each multiple of `movePct`, while its market is open, kept up to date through the day and muted once it falls back; a price crossing a target |
| `nfl` | ESPN scoreboard; the summary is a chip per game today, followed teams' highlighted | kick-off, scores and results for followed teams (a touchdown's score waits up to two minutes for the try, so it's notified once); optionally every result |
| `weather` | [Open-Meteo](https://open-meteo.com) (no key needed); the summary is the next twelve hours | rain due within the hour, frost, strong gusts |
| `autotrader` | AutoTrader UK search | newly listed cars |
| `todo` | a to-do list kept on the server, changed from clients | nothing; ticked tasks clear after `clearAfter` (default `24h`) |
| `chores` | recurring chores planned around each other (below), ticked off from clients | each morning at `remindAt` (default `08:00`), the day's chores |
| `fantasy` | Sleeper and ESPN fantasy leagues, and ESPN's play-by-play | notable plays by players on any of your teams: touchdowns, big gains, turnovers, long or missed kicks, and your defences' takeaways, sacks and scores |
| `calendar` | iCalendar (ICS) feeds, e.g. Google or Outlook | each timed event, `remindMinutes` (default 10) before it starts |
| `football` | `nfl` and `fantasy` together as one panel (see below) | what each of them notifies |
| `mail` | IMAP (e.g. Gmail) and Outlook mailboxes, read by Gemini for bookings, orders and things to do, which it adds to the calendar, to-do list and chores (below) | nothing itself (each finding is a quiet event); a sign-in code when an Outlook account needs one |
| `dailies` | a dailies tracker (a separate app)'s games and today's results: every game, linked and marked once `player` has logged it; the summary is a chip for each (`names` gives them shorter names by game ID), green once played. With `versus` set to the other player, a played game is instead green when won, red when lost and blue when drawn or still waiting on them (the tracker says who won). On the tracker's co-op days, played games are green when they hit the day's target and red when they miss it. When the tracker has haikudle's haiku of the day, it comes last, green once `player` has written theirs (never won or lost). Its paste button logs a game's share text from the clipboard | what happened to each paste, logged or not and why, as a temporary event |
| `ebay` | an eBay search, newest listings first, through eBay's Browse API (below): what each listing is (named by Gemini from its keyword-stuffed title, with `gemini`; else the title; uncommon and rare ones say so, with a button that ignores the game from then on: action `ignore` with the listing's ID, after which its listings count as common), its price (an auction's current bid, with the bids) and when it went up; short, the newest three rows | each new listing, with its picture; none for those up at the first check or those Gemini rates below `notify`, which are only in the panel |
| `letterboxd` | a Letterboxd `user`'s public watchlist, newest additions first, as a table linking each film; short, it's the newest three rows. With `local` set to an app's name (`watchgrab`), each row also has that app's button, which only clients with the app installed draw (below). It reads the whole list every `fullEvery` (default `1h`) and only the first page, where additions appear, in between, so it can poll often (every 10 minutes, say) | each film added, as a quiet event |
| `osrs` | [Wise Old Man](https://wiseoldman.net): an Old School RuneScape `player`'s gains since midnight towards pets only: boss kills, Guardians of the Rift, Soul Wars zeal, master clues, and XP in the skills with a pet (not combat). It asks WOM to update the player each poll and just after midnight, and compares with the last snapshot from before midnight. The table gives each gain's pet drop rate (from the wiki) and, for bosses that have ever given a drop worth `bigLoot` or a pet, the kills since the last one. Short, it's the table's top three rows (`summaryBlock`); the chips are for clients without that. With `hookSecret` set, the RuneLite plugin [Dink](https://github.com/pajlads/DinkPlugin) can make it live (below): kills count as they happen, and drops worth `bigLoot` or more (default 20m) and pets are listed and notify | `hookSecret` (e.g. `"${OSRS_HOOK_SECRET}"`) |

Each monitor also returns a dashboard panel and a short summary (shown when
space is short), polls on its own interval and can ask to be polled sooner
(the NFL monitor polls every 30 seconds while a game is on, the fantasy
monitor every 30 seconds while your players are playing).

### Fantasy

The `fantasy` monitor reads rosters from Sleeper (`sleeperUser`: every
league that user is in this season) and ESPN (`espn`: a `leagueId`, plus
the `espn_s2` and `SWID` cookies from a signed-in browser as `s2` and
`swid` for private leagues; `SWID` also identifies your team). Sleeper
players are matched to ESPN athletes by Sleeper's ESPN ID where it has
one, else by name and team. During games it reads ESPN's play-by-play for
games involving your players or team defences, and notifies touchdowns,
gains of `bigPlayYards` (default 20) or more, interceptions thrown,
fumbles lost, 50+ yard and missed kicks, and your defences' takeaways,
sacks, safeties and scores. Plays called back for penalties don't count.
Its summary is a chip per league with your live matchup score (the
week Sleeper shows, which stays on the week just played until midweek, so
Monday night's results are still there the morning after; until it moves
on, Sleeper is asked every 15 minutes); `names` gives leagues shorter
names by ID. Rosters are read again every six hours.

### Football

The `football` monitor runs an `nfl` and a `fantasy` monitor (their
configs as `nfl` and `fantasy`) as one panel. Its summary is a chip per
fantasy league, then at most `games` (default 4) of the week's games, on
one line each: the followed teams' first, then today's games with any of
your fantasy players in them, live ones first, then those to come, then
those over. Its panel is the fantasy matchups, the NFL's week of scores,
then your players' plays, and each part keeps its own state (under `nfl/`
and `fantasy/`).

### Portfolio

The `stocks` monitor can also estimate an account's value and the day's
gain or loss from the same quotes: give it a `portfolio` with `cash` and
`positions`. Each position has `units` of a CNBC `symbol`, priced live and
converted to pounds (USD at CNBC's GBP/USD rate, London prices in pence),
or a fixed `value` in pounds for anything CNBC doesn't quote, e.g. a fund,
which moves by a `symbol`'s daily percentage if one is given (an index for
a tracker). Today's gain counts each position's move from today's
session, or its last session's if its market hasn't opened yet (so before
New York opens it has last night's US moves alongside London's); like a
broker's own figure it leaves out currency moves. Recent days record only
moves from that day's sessions. The panel shows the value, today's gain,
the positions moving it most today and recent days (kept for 30), and the
summary leads with today's gain, marked `lead`. It's all marked private,
for clients to hide.

```json
"portfolio": {
  "cash": 120.50,
  "positions": [
    { "symbol": "AAPL", "units": 10 },
    { "symbol": "BP.-GB", "units": 200 },
    { "name": "FTSE 250 tracker", "symbol": ".FTMC", "value": 1500 },
    { "name": "Managed fund", "value": 800 }
  ]
}
```

### Chores

The `chores` monitor is a to-do list that fills itself. Each chore is due
`every` so many days after it was last done, so doing one late or early
moves the next one with it. Chores that share something (`uses`, e.g. the
washing machine) are spread out so no day has more of them than `perDay`
allows (one each, unless set), and each goes as close to its due day as it
can: the most pressing first, within `flex` days either side (default 1)
before it counts as late, and on its preferred `days` if it has them.
Chores never done start straight away, staggered the same way.

Its panel is two checklists, *Today* and *Coming up* (with the day each is
planned for). Ticking a chore marks it done today, even one not due yet;
unticking it the same day undoes that. Its up and down buttons bring it
forward or put it off a day (a `move` action), and the rest fit around
it; clients without them can put it off with `delete`. Its summary is
only today's chores, each marked done or not (or "Nothing to do"); what's
coming up stays in the panel. It looks like the to-do list's.

```json
"config": {
  "perDay": { "washer": 1 },
  "chores": [
    { "name": "Towel wash", "every": 7, "uses": "washer" },
    { "name": "Bedsheet wash", "every": 14, "flex": 3, "days": ["Sat", "Sun"], "uses": "washer" },
    { "name": "Bins out", "every": 7, "flex": 0, "days": ["Thu"] }
  ]
}
```

A chore's history is kept by its `id`, which defaults to its name in
lower case with dashes, so give it the old `id` when renaming it.

Other monitors (the `mail` monitor) can also tell it a chore was done on
a given day (a `done` action, with `{"day": "2026-10-12"}`), and track
chores of their own with a `track` action (`{"name", "every", "last"}`),
for reordering things bought regularly. Those go in a third checklist,
*Reorders*, due when the last lot has likely run out; ticking one means
it's been ordered, and its delete button stops tracking it for good.

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
calendar shows without them. Naming places you go regularly in the
`context` ("their local cinema is the Odeon in Bristol") lets Gemini fill
in a film's screening or a venue for titles that don't say; changing the
`context` makes every note again.

Other monitors can add events too (the `mail` monitor's bookings): an
`upsert` action with a `calendar.Added` as its data adds or replaces one
by its `key`, and `remove` takes it away. They show alongside the feeds',
with their `source` and own `detail` in place of a Gemini note, and
reminders like any other event. One a feed already has (the same start,
and a word in common) is left out.

Each feed's address is a secret, so put it in the environment:

- **Google Calendar**: Settings → the calendar → *Integrate calendar* →
  *Secret address in iCal format*.
- **Outlook / Microsoft 365**: Settings → Calendar → *Shared calendars* →
  *Publish a calendar*, with *Can view all details*, then copy the ICS link.
  Some work accounts don't allow publishing.

### Mail

The `mail` monitor reads your email for what belongs on the dashboard,
and shows none of the mail itself. Each new message whose subject looks
like an order, booking, ticket, appointment, delivery or a change to one
is passed, with its text, to Gemini; nothing else leaves the server.
Gemini says what in it is worth keeping, and the monitor passes that on:

- **Calendar**: tickets, reservations, travel, appointments and booked
  delivery slots, as events from "Email"; a later email about the same
  booking (by its reference) moves or cancels it.
- **To do**: things the email asks you to do by a date, such as returning
  something or checking in.
- **Chores**: the chores in `choresDone` ticked off on the day of an email
  that shows them done (placing the weekly shop), and everyday things
  bought again and again (dishwasher tablets, coffee) tracked as
  reorders, due again after the time between your orders, or Gemini's
  guess until there are two.

The first time, it reads back `lookbackDays` (default 365), which finds
bookings still to come and how often you reorder things; past events and
old to-dos are skipped. Its panel, which clients keep with their settings
rather than on the dashboard (unless an account needs signing in), lists
what each email added.

```json
"config": {
  "accounts": [
    { "type": "imap", "name": "Gmail", "user": "${GMAIL_USER}", "password": "${GMAIL_APP_PASSWORD}" },
    { "type": "outlook", "name": "Outlook", "clientId": "${OUTLOOK_CLIENT_ID}" }
  ],
  "gemini": { "apiKey": "${GEMINI_API_KEY}", "context": "Lives with their partner in Bristol." },
  "choresDone": { "waitrose-order": "a Waitrose groceries order being placed" }
}
```

- **Gmail** (`imap`, `imap.gmail.com:993` by default): turn on 2-Step
  Verification, then make an app password at
  [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords).
  It reads `INBOX` unless `folder` says otherwise, and never marks mail
  read.
- **Outlook** (`outlook`, through Microsoft Graph): Outlook.com no longer
  takes passwords, so register an app in the
  [Microsoft Entra admin centre](https://entra.microsoft.com) (App
  registrations → New registration; *Personal Microsoft accounts only*; no
  redirect URI), turn on *Allow public client flows* under
  Authentication, and give its *Application (client) ID* as `clientId`.
  The panel then shows a code to enter at microsoft.com/devicelogin; once
  signed in, the monitor stays signed in. For a work or school account,
  set `tenant` (it defaults to `consumers`).

`calendar`, `todo` and `chores` name the monitors to add to (by default
those IDs; empty leaves one out), `reorders: false` turns reorders off,
and `ignoreFrom` skips senders whose address contains any of its strings.

### eBay

eBay turns away requests for its search pages that don't come from a
browser, so the `ebay` monitor uses its official Browse API, which needs a
free developer account: at [developer.ebay.com](https://developer.ebay.com)
create an application keyset for **Production**, and give its App ID and
Cert ID as `clientId` and `clientSecret`. The search is a `searchURL`
copied from the site (its keywords and category are used; the sort is
always newest first) or a `query` and `category`. `name` says what's
listed, to help Gemini name things. Gemini also rates each listing
`common`, `uncommon` or `rare` for a collector, from what it knows of the
thing itself (not the seller's "RARE" or the price): rare is a famous game
(often a console one) whose boxed PC copy seldom turns up, or a special
version of a box; uncommon the same, less striking; everything else
(DOS-era PC classics, expansions, sealed copies, other platforms, obscure
games, anything after about 2004) is common; `notify` is the least rarity that
notifies; the rest make no event and are only in the panel (empty: every new listing).

```json
"config": {
  "searchURL": "https://www.ebay.co.uk/sch/i.html?_nkw=big+box+pc&_sacat=0",
  "clientId": "${EBAY_CLIENT_ID}", "clientSecret": "${EBAY_CLIENT_SECRET}",
  "name": "a boxed PC game", "notify": "uncommon",
  "gemini": { "apiKey": "${GEMINI_API_KEY}" }
}
```

### OSRS

Wise Old Man only knows what the hiscores said when it last updated, so
on its own the panel lags by up to the poll interval. For live updates,
install [Dink](https://runelite.net/plugin-hub/show/dink) from the
RuneLite plugin hub and add the monitor's hook,
`https://<host>/api/hooks/osrs/<hookSecret>`, to its webhook URLs (one per
line, so it can still post to Discord). Then turn on:

- **Kill Count**, with the interval at 1: every boss kill, raid and
  Tempoross or Wintertodt count.
- **Loot**, with **Min Loot value** (an item's) at the panel's `bigLoot`
  (20m) or lower. Drops worth `bigLoot` in all are listed and notify.
- **Clue**, for master clue counts and big clue rewards.
- **Pet**: listed and notified at high priority.
- For skill XP at each login, put the same URL in **Advanced → Custom
  Metadata Handler**.

Notifications from other accounts and from leagues or Deadman worlds are
ignored. Each one has the monitor poll, but it asks WOM to update at most
hourly (the hiscores only change at logout), so skill XP between logins
comes from Dink's login summary. Kill counts
for bosses the hiscores don't rank yet start from the first kill Dink
reports that day, rather than counting every earlier kill as today's.
Big drops from before Dink can be filled in with a `lastBig` action,
`{"type":"lastBig","data":{"vorkath":1020}}`: the kill count of the last
one by WOM metric (RuneLite's Loot Logger plugin keeps the history, with
each drop's price at the time). Screenshots are dropped; turn them off unless Dink also posts to
Discord, as bodies over 1MB are refused.

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
| `POST /api/monitors/{id}/actions` | 204; applies a JSON action such as `{"type":"add","text":"Bins"}`, `{"type":"toggle","id":"…"}` or `{"type":"delete","id":"…"}` (some take a `data` object too), then polls. 400 for an action the monitor refuses, 405 for a monitor that takes none |
| `POST /api/hooks/{id}/{secret}` | 204; a webhook for a monitor that takes them (`osrs`), with the monitor's secret in the URL instead of the token. The body is JSON, or a Discord-style multipart form whose `payload_json` part is (other parts, such as screenshots, are dropped) |
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
`priority` (`low`, `normal`, `high`), `quiet`, `updated` and `expires`. Quiet events go
in the history but shouldn't raise a notification. An event with `expires` is
temporary: it matters only until then (the dailies' paste outcomes), so
clients show it apart from the history while it lasts, then drop it. A monitor can update an
event it raised earlier (e.g. a price move that's since changed); the
stream then sends it again with the same `id` and an `updated` time, and
clients replace their copy without notifying. A client that reconnects
with `since` also gets the events it had that were updated after event
`since`.

**Snapshot**: `monitor`, `kind`, `title`, `updated`, `error` (the last
poll's, with the last good blocks kept), `subtitle` (what it's watching, when that isn't obvious, e.g. a car
search's filters), `timely` (true while when it was updated matters: always
for the watchlist and AutoTrader, while a game's on for NFL and fantasy;
the Windows client only shows a panel's age then, on hover), `relevant`
(`from`, which may be absent, and `until`: when the panel matters most,
e.g. while a game's on, from an hour before a calendar event until it ends,
or for half an hour after Dink last heard from the OSRS player; the Windows
client moves it under its Recent column then), `background`
(true for a monitor that only feeds others, like `mail`: clients leave it
off the dashboard and show it with their settings), `paste` (if set, the
panel has a button, with this as its tooltip, that sends a `paste` action
with the clipboard's text, e.g. the dailies' share text), `blocks` and `summary`: a few
stats (as in a `stats` block) worth seeing at a glance, which clients show
instead of the blocks when space is short, and `summaryStyle`: empty for
label/value/detail tiles, or `chips` for small blocks of the same, filled
with each stat's tone (the watchlist's daily moves, the hourly forecast,
today's NFL games). `summaryBlock`, if set, is one block that clients
able to draw it show in place of the summary, so the panel looks the same
short (the OSRS table's top rows). A stat or block marked `private` holds personal
figures, e.g. money, which clients can hide; a stat marked `lead` is set
apart from the ones after it (the portfolio's day before the watchlist's
moves), and one with a `url` opens it when clicked (the dailies' chips).
Every monitor is
drawn from the same few block types, so clients need no per-monitor code:

| Block `type` | Fields |
| --- | --- |
| `stats` | `stats`: `label`, `value`, `detail`, `tone`, `series` (a sparkline), `icon` (a weather picture by the value: `sun`, `moon`, `sun-cloud`, `moon-cloud`, `cloud`, `fog`, `drizzle`, `rain`, `showers`, `snow` or `storm`), `level` (0 to 1, drawn faintly behind the chip as a chart joined to its neighbours', e.g. the chance of rain; 0 draws along the bottom, absent draws nothing) |
| `table` | `columns` (`title`, `align`, `fill` for the column that takes spare width) and `rows` of cells (`text`, `tone`, `url`, `detail` for a smaller line under the text, and `local`: `{"app", "arg"}`, a button for an app on the client's own machine, e.g. the watchlist's downloader, drawn only by clients that have it; and `action`: `{"type", "id", "label"}`, a button that sends the monitor that action, with `label` saying what it does, e.g. eBay's ignore) |
| `list` | `items`: `title`, `subtitle`, `detail`, `url`, `image`, `tone` |
| `checklist` | `items` (`id`, `title`, `done`, `tone`) with checkboxes that send `toggle` and `delete` actions; `addHint` adds a box for `add`, `deleteHint` names what delete does when it isn't deleting (the reorders' "Stop tracking") and `noDelete` leaves it out; `movable` adds up and down buttons that send `move` with text `up` or `down` (reordering the to-do list), named by `upHint` and `downHint` when they do something else (the chores' days) |
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
- A monitor that takes webhooks also implements `monitor.Hooked`: its
  `HookSecret` guards `POST /api/hooks/{id}/{secret}`, and each hook
  arrives at `Act` as type `monitor.HookAction` with the JSON as data.
- A monitor can act on another, as a client would, with `env.Act` (the
  `mail` monitor adds to the calendar this way). It's nil without an
  engine, and a monitor can't act on itself.
- Event keys only need to be unique within the monitor. The engine drops
  any key it has seen before, so a monitor can report the current state on
  every poll without repeating notifications.
- `monitor/monitortest` has an in-memory `State`, an `Env` with a fixed
  clock and a server that replays saved API responses; the bundled
  monitors' tests show how to use them.

## Licence

MIT
