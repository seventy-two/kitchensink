package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := vars[k]
		return v, ok
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`{
		"token": "${TOKEN}",
		"monitors": [
			{"type": "stocks", "interval": "5m", "config": {"symbols": ["AAPL"]}},
			{"id": "car", "type": "autotrader", "title": "Car", "config": {"searchURL": "${URL}"}}
		]
	}`), env(map[string]string{"TOKEN": "secret", "URL": `https://example.com/?a="b"&c=d`}))
	require.NoError(t, err)

	assert.Equal(t, "secret", c.Token)
	assert.Equal(t, "127.0.0.1:6030", c.Listen)
	require.Len(t, c.Monitors, 2)
	assert.Equal(t, "stocks", c.Monitors[0].ID, "ID defaults to the type")
	assert.Equal(t, "stocks", c.Monitors[0].Title)
	assert.Equal(t, 5*time.Minute, time.Duration(c.Monitors[0].Interval))
	assert.Equal(t, "Car", c.Monitors[1].Title)
	assert.JSONEq(t, `{"searchURL": "https://example.com/?a=\"b\"&c=d"}`, string(c.Monitors[1].Config), "env values are JSON-escaped")
}

func TestParseErrors(t *testing.T) {
	for name, src := range map[string]string{
		"unset variable": `{"token": "${NOPE}"}`,
		"no token":       `{}`,
		"unknown field":  `{"token": "x", "lisen": ":1"}`,
		"no type":        `{"token": "x", "monitors": [{}]}`,
		"bad duration":   `{"token": "x", "monitors": [{"type": "a", "interval": 5}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(src), env(nil))
			assert.Error(t, err)
		})
	}
}
