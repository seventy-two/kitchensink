package stocks

import (
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

// latestKey is the state key holding each symbol's newest quote seen.
const latestKey = "latest"

// quoteTime is the layout of a quote's last_time.
const quoteTime = "2006-01-02T15:04:05.000-0700"

// newest replaces any quote older than one already seen with that one, or
// without a price, and
// remembers the newest of each. CNBC's servers don't all agree: on 19 Nov
// 2026, minutes after the open, one poll got the day before's NVDA quote
// (-2.89%, and a portfolio day to match) between two of that morning's.
func newest(env monitor.Env, quotes []quote) []quote {
	seen := map[string]quote{}
	if _, err := env.State.Get(latestKey, &seen); err != nil {
		env.Log.Warn("reading latest quotes", "err", err)
	}
	out := make([]quote, len(quotes))
	for i, q := range quotes {
		out[i] = q
		if _, ok := parseNumber(q.Last); !ok {
			// Blank: on 23 Nov 2026 CNBC sent some quotes (GBP=, the
			// London ones) with no prices at all, which left the
			// portfolio without every US share.
			if old, ok := seen[q.Symbol]; ok {
				env.Log.Warn("quote without a price, keeping the last one", "symbol", q.Symbol, "had", old.LastTime)
				fixPence(&old) // saved before fixPence existed
				out[i] = old
			}
			continue
		}
		got, err := time.Parse(quoteTime, q.LastTime)
		if err != nil {
			continue
		}
		if old, ok := seen[q.Symbol]; ok {
			if had, err := time.Parse(quoteTime, old.LastTime); err == nil && got.Before(had) {
				env.Log.Warn("older quote than seen, keeping the newer", "symbol", q.Symbol, "got", q.LastTime, "had", old.LastTime)
				out[i] = old
				continue
			}
		}
		seen[q.Symbol] = q
	}
	if err := env.State.Set(latestKey, seen); err != nil {
		env.Log.Warn("saving latest quotes", "err", err)
	}
	return out
}
