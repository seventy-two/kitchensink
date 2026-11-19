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
