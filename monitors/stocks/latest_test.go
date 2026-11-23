package stocks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/seventy-two/kitchensink/monitor/monitortest"
)

func TestNewestKeepsTheNewerQuote(t *testing.T) {
	env := monitortest.Env(time.Date(2026, 11, 19, 14, 35, 0, 0, time.UTC))
	today := quote{Symbol: "NVDA", Last: "156.90", ChangePct: "+3.94%", LastTime: "2026-11-19T09:34:10.000-0500"}
	yesterday := quote{Symbol: "NVDA", Last: "150.96", ChangePct: "-2.89%", LastTime: "2026-11-18T16:00:00.000-0500"}
	later := quote{Symbol: "NVDA", Last: "157.08", ChangePct: "+4.11%", LastTime: "2026-11-19T09:37:40.927-0500"}

	assert.Equal(t, today, newest(env, []quote{today})[0])
	assert.Equal(t, today, newest(env, []quote{yesterday})[0], "the day before's, served late")
	assert.Equal(t, later, newest(env, []quote{later})[0])

	// Unreadable times pass through.
	odd := quote{Symbol: "X", LastTime: "?"}
	assert.Equal(t, odd, newest(env, []quote{odd})[0])
}

func TestNewestTakesTheClosingPrint(t *testing.T) {
	env := monitortest.Env(time.Date(2026, 11, 23, 21, 34, 0, 0, time.UTC))
	tick := quote{Symbol: "NVDA", Last: "165.19", MarketStatus: "REG_MKT", LastTime: "2026-11-23T16:00:00.179-0500"}
	closed := quote{Symbol: "NVDA", Last: "165.19", MarketStatus: "POST_MKT", LastTime: "2026-11-23T16:00:00.000-0500"}

	newest(env, []quote{tick})
	assert.Equal(t, closed, newest(env, []quote{closed})[0], "stamped on the second, ms before the last tick")
}

func TestNewestFillsBlankQuotes(t *testing.T) {
	env := monitortest.Env(time.Date(2026, 11, 23, 15, 57, 0, 0, time.UTC))
	fx := quote{Symbol: "GBP=", Last: "1.3041", Change: "-0.004", LastTime: "2026-11-23T10:04:00.000-0500"}
	spx := quote{Symbol: "SPX5-GB", Last: "55,673.14", Change: "+1,812.14", PrevClose: "53,861.00", CurrencyCode: "GBP",
		LastTime: "2026-11-23T15:48:28.000+0000"} // saved in pence by the old build
	newest(env, []quote{fx, spx})

	got := newest(env, []quote{{Symbol: "GBP=", Change: "0.00"}, {Symbol: "SPX5-GB", CurrencyCode: "GBP"}, {Symbol: "NEW"}})
	assert.Equal(t, fx, got[0])
	assert.Equal(t, "556.73", got[1].Last, "and put in pounds")
	assert.Equal(t, quote{Symbol: "NEW"}, got[2], "nothing to fall back on")
}
