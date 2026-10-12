// Package weather shows the forecast for one place from Open-Meteo, which
// needs no API key, and notifies rain arriving soon, frost and strong winds.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/seventy-two/kitchensink/monitor"
)

func init() {
	monitor.Register("weather", func(c json.RawMessage) (monitor.Monitor, error) { return New(c) })
}

const defaultForecastURL = "https://api.open-meteo.com/v1/forecast"

// Config is the monitor's config.
type Config struct {
	// Place is the name shown in notifications.
	Place     string  `json:"place"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// Timezone is an IANA zone such as "Europe/London". It defaults to
	// the place's own, as Open-Meteo works it out.
	Timezone string `json:"timezone"`
	// RainMM notifies when this much rain per 15 minutes is due in the
	// next hour while it's dry. It defaults to 0.2; 0 turns it off.
	RainMM *float64 `json:"rainMM"`
	// FrostC notifies when today's or tomorrow's low is at or below this.
	// Leave it out to turn frost alerts off.
	FrostC *float64 `json:"frostC"`
	// GustMPH notifies when today's or tomorrow's gusts reach this. It
	// defaults to 50; 0 turns it off.
	GustMPH *float64 `json:"gustMPH"`
	// ForecastURL replaces Open-Meteo's endpoint, for tests.
	ForecastURL string `json:"forecastURL"`
}

// Monitor is the weather monitor.
type Monitor struct {
	cfg             Config
	rainMM, gustMPH float64
}

// New builds the monitor from its JSON config.
func New(raw json.RawMessage) (*Monitor, error) {
	var c Config
	if err := monitor.Decode(raw, &c); err != nil {
		return nil, err
	}
	if c.Latitude == 0 && c.Longitude == 0 {
		return nil, fmt.Errorf("weather needs a latitude and longitude")
	}
	if c.Place == "" {
		c.Place = "your area"
	}
	if c.Timezone == "" {
		c.Timezone = "auto"
	}
	if c.ForecastURL == "" {
		c.ForecastURL = defaultForecastURL
	}
	m := &Monitor{cfg: c, rainMM: 0.2, gustMPH: 50}
	if c.RainMM != nil {
		m.rainMM = *c.RainMM
	}
	if c.GustMPH != nil {
		m.gustMPH = *c.GustMPH
	}
	return m, nil
}

type forecast struct {
	Current struct {
		Time        string  `json:"time"`
		Temperature float64 `json:"temperature_2m"`
		FeelsLike   float64 `json:"apparent_temperature"`
		Code        int     `json:"weather_code"`
		Wind        float64 `json:"wind_speed_10m"`
		Gusts       float64 `json:"wind_gusts_10m"`
		Rain        float64 `json:"precipitation"`
		IsDay       int     `json:"is_day"`
	} `json:"current"`
	Minutely15 struct {
		Time []string  `json:"time"`
		Rain []float64 `json:"precipitation"`
	} `json:"minutely_15"`
	Hourly struct {
		Time        []string  `json:"time"`
		Temperature []float64 `json:"temperature_2m"`
		RainProb    []float64 `json:"precipitation_probability"`
		Code        []int     `json:"weather_code"`
		Gusts       []float64 `json:"wind_gusts_10m"`
		IsDay       []int     `json:"is_day"`
	} `json:"hourly"`
	Daily struct {
		Time     []string  `json:"time"`
		Code     []int     `json:"weather_code"`
		High     []float64 `json:"temperature_2m_max"`
		Low      []float64 `json:"temperature_2m_min"`
		RainProb []float64 `json:"precipitation_probability_max"`
		Gusts    []float64 `json:"wind_gusts_10m_max"`
	} `json:"daily"`
}

func (m *Monitor) fetch(ctx context.Context, client *http.Client) (*forecast, error) {
	q := url.Values{
		"latitude":             {strconv.FormatFloat(m.cfg.Latitude, 'f', -1, 64)},
		"longitude":            {strconv.FormatFloat(m.cfg.Longitude, 'f', -1, 64)},
		"timezone":             {m.cfg.Timezone},
		"current":              {"temperature_2m,apparent_temperature,weather_code,wind_speed_10m,wind_gusts_10m,precipitation,is_day"},
		"minutely_15":          {"precipitation"},
		"forecast_minutely_15": {"8"},
		"hourly":               {"temperature_2m,precipitation_probability,weather_code,wind_gusts_10m,is_day"},
		"forecast_hours":       {strconv.Itoa(hours)},
		"daily":                {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,wind_gusts_10m_max"},
		"forecast_days":        {"5"},
		"wind_speed_unit":      {"mph"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.ForecastURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("forecast returned %s", resp.Status)
	}
	f := &forecast{}
	if err := json.NewDecoder(resp.Body).Decode(f); err != nil {
		return nil, fmt.Errorf("decoding forecast: %w", err)
	}
	return f, nil
}

func (m *Monitor) Poll(ctx context.Context, env monitor.Env) (monitor.Result, error) {
	f, err := m.fetch(ctx, env.HTTP)
	if err != nil {
		return monitor.Result{}, err
	}
	return monitor.Result{Blocks: m.blocks(f), Summary: m.summary(f), SummaryStyle: monitor.SummaryChips, Events: m.events(f)}, nil
}

// hours is how far ahead the hourly forecast goes, from this hour.
const hours = 12

// hour is one hour of the forecast.
type hour struct {
	label, temp string
	sky         string // e.g. "Cloudy"
	icon        string
	rainProb    float64
	tone        monitor.Tone
}

// hourly reads the hourly forecast: the first hour is now, at the current
// temperature. Hours likely to rain are marked, and strong gusts more so.
func (m *Monitor) hourly(f *forecast) []hour {
	h := f.Hourly
	var out []hour
	for i, t := range h.Time {
		if i >= hours || i >= len(h.Temperature) || i >= len(h.RainProb) || i >= len(h.Code) || i >= len(h.Gusts) {
			break
		}
		day := i < len(h.IsDay) && h.IsDay[i] == 1
		sky := brief(h.Code[i], day)
		x := hour{label: clock(t), temp: fmt.Sprintf("%.0f°", h.Temperature[i]), sky: sky, icon: icon(h.Code[i], day), rainProb: h.RainProb[i]}
		if i == 0 {
			x.label = "Now"
			x.temp = fmt.Sprintf("%.0f°", f.Current.Temperature)
		}
		switch {
		case m.gustMPH > 0 && h.Gusts[i] >= m.gustMPH:
			x.tone = monitor.ToneWarn
		case h.RainProb[i] >= 50:
			x.tone = monitor.ToneAccent
		}
		out = append(out, x)
	}
	return out
}

// summary is the hourly forecast, an hour to a chip: the sky as an icon,
// and the chance of rain as a chart behind them.
func (m *Monitor) summary(f *forecast) []monitor.Stat {
	var out []monitor.Stat
	for _, h := range m.hourly(f) {
		out = append(out, monitor.Stat{Label: h.label, Value: h.temp, Tone: h.tone, Icon: h.icon, Level: ptr(h.rainProb / 100)})
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// rainStat sums the next two hours' rain, with the series as a sparkline.
func rainStat(series []float64) monitor.Stat {
	rain := monitor.Stat{Label: "Rain, next 2h", Value: "Dry", Series: series}
	var total float64
	for _, r := range series {
		total += r
	}
	if total > 0 {
		rain.Value = fmt.Sprintf("%.1f mm", total)
		rain.Tone = monitor.ToneAccent
	}
	return rain
}

// events checks the forecast against the alert thresholds. Keys hold the
// day, or the hour for rain, so each alert fires once.
func (m *Monitor) events(f *forecast) []monitor.Event {
	var events []monitor.Event
	if m.rainMM > 0 && f.Current.Rain < m.rainMM {
		// The first slot is the current quarter hour; look an hour ahead.
		for i, t := range f.Minutely15.Time {
			if i >= 5 || i >= len(f.Minutely15.Rain) {
				break
			}
			if f.Minutely15.Rain[i] >= m.rainMM {
				// Keyed by hour so a start time that wobbles between
				// polls doesn't notify again.
				events = append(events, monitor.Event{
					Key:   "rain:" + hourOf(t),
					Title: "Rain in " + m.cfg.Place + " around " + clock(t),
					Body:  fmt.Sprintf("%.1f mm expected in that quarter hour", f.Minutely15.Rain[i]),
				})
				break
			}
		}
	}
	for i, day := range f.Daily.Time {
		if i > 1 {
			break // today and tomorrow only
		}
		when := strings.ToLower(dayName(day, i))
		if m.cfg.FrostC != nil && i < len(f.Daily.Low) && f.Daily.Low[i] <= *m.cfg.FrostC {
			events = append(events, monitor.Event{
				Key:   "frost:" + day,
				Title: fmt.Sprintf("Frost %s in %s", when, m.cfg.Place),
				Body:  fmt.Sprintf("Low of %.0f°C", f.Daily.Low[i]),
			})
		}
		if m.gustMPH > 0 && i < len(f.Daily.Gusts) && f.Daily.Gusts[i] >= m.gustMPH {
			events = append(events, monitor.Event{
				Key:      "wind:" + day,
				Title:    fmt.Sprintf("Strong winds %s in %s", when, m.cfg.Place),
				Body:     fmt.Sprintf("Gusts up to %.0f mph", f.Daily.Gusts[i]),
				Priority: monitor.PriorityHigh,
			})
		}
	}
	return events
}

func (m *Monitor) blocks(f *forecast) []monitor.Block {
	c := f.Current
	rain := rainStat(f.Minutely15.Rain)
	now := monitor.Block{
		Type:  monitor.BlockStats,
		Title: m.cfg.Place,
		Stats: []monitor.Stat{
			{Label: "Now", Value: fmt.Sprintf("%.0f°C", c.Temperature), Detail: describe(c.Code, c.IsDay == 1), Icon: icon(c.Code, c.IsDay == 1)},
			{Label: "Feels like", Value: fmt.Sprintf("%.0f°C", c.FeelsLike)},
			{Label: "Wind", Value: fmt.Sprintf("%.0f mph", c.Wind), Detail: fmt.Sprintf("gusts %.0f", c.Gusts), Tone: m.windTone(c.Gusts)},
			rain,
		},
	}

	days := monitor.Block{
		Type: monitor.BlockTable,
		Columns: []monitor.Column{
			{Title: "Day"},
			{Title: "Weather"},
			{Title: "High", Align: monitor.AlignRight},
			{Title: "Low", Align: monitor.AlignRight},
			{Title: "Rain", Align: monitor.AlignRight},
			{Title: "Gusts", Align: monitor.AlignRight},
		},
		Rows: [][]monitor.Cell{},
	}
	d := f.Daily
	for i, day := range d.Time {
		if i >= len(d.Code) || i >= len(d.High) || i >= len(d.Low) || i >= len(d.RainProb) || i >= len(d.Gusts) {
			break
		}
		lowTone := monitor.ToneNone
		if m.cfg.FrostC != nil && d.Low[i] <= *m.cfg.FrostC {
			lowTone = monitor.ToneAccent
		}
		days.Rows = append(days.Rows, []monitor.Cell{
			{Text: dayName(day, i)},
			{Text: describe(d.Code[i], true), Tone: monitor.ToneMuted},
			{Text: fmt.Sprintf("%.0f°", d.High[i])},
			{Text: fmt.Sprintf("%.0f°", d.Low[i]), Tone: lowTone},
			{Text: fmt.Sprintf("%.0f%%", d.RainProb[i])},
			{Text: fmt.Sprintf("%.0f mph", d.Gusts[i]), Tone: m.windTone(d.Gusts[i])},
		})
	}
	// The hours across, then the days down.
	hourly := monitor.Block{Type: monitor.BlockTable, Columns: []monitor.Column{{}}, Rows: [][]monitor.Cell{}}
	temps := []monitor.Cell{{Text: "Temp", Tone: monitor.ToneMuted}}
	conds := []monitor.Cell{{Text: "Weather", Tone: monitor.ToneMuted}}
	rains := []monitor.Cell{{Text: "Rain", Tone: monitor.ToneMuted}}
	for _, h := range m.hourly(f) {
		hourly.Columns = append(hourly.Columns, monitor.Column{Title: h.label, Align: monitor.AlignRight})
		temps = append(temps, monitor.Cell{Text: h.temp, Tone: h.tone})
		conds = append(conds, monitor.Cell{Text: h.sky, Tone: monitor.ToneMuted})
		rains = append(rains, monitor.Cell{Text: fmt.Sprintf("%.0f%%", h.rainProb), Tone: h.tone})
	}
	hourly.Rows = append(hourly.Rows, temps, conds, rains)
	return []monitor.Block{now, hourly, days}
}

// brief names a WMO weather code in a word or two, for the hourly
// forecast.
func brief(code int, day bool) string {
	switch {
	case code <= 1:
		if day {
			return "Sunny"
		}
		return "Clear"
	case code == 2:
		return "Part cloud"
	case code == 3:
		return "Cloudy"
	case code == 45 || code == 48:
		return "Fog"
	case code >= 51 && code <= 57:
		return "Drizzle"
	case code >= 61 && code <= 67:
		return "Rain"
	case code >= 71 && code <= 77, code == 85, code == 86:
		return "Snow"
	case code >= 80 && code <= 82:
		return "Showers"
	case code >= 95:
		return "Storms"
	}
	return describe(code, day)
}

// icon names a picture for a WMO weather code (see monitor.Stat.Icon).
func icon(code int, day bool) string {
	switch {
	case code <= 1:
		if day {
			return "sun"
		}
		return "moon"
	case code == 2:
		if day {
			return "sun-cloud"
		}
		return "moon-cloud"
	case code == 3:
		return "cloud"
	case code == 45 || code == 48:
		return "fog"
	case code >= 51 && code <= 57:
		return "drizzle"
	case code >= 61 && code <= 67:
		return "rain"
	case code >= 71 && code <= 77, code == 85, code == 86:
		return "snow"
	case code >= 80 && code <= 82:
		if day {
			return "showers"
		}
		return "rain"
	case code >= 95:
		return "storm"
	}
	return ""
}

func (m *Monitor) windTone(gust float64) monitor.Tone {
	if m.gustMPH > 0 && gust >= m.gustMPH {
		return monitor.ToneWarn
	}
	return monitor.ToneNone
}

// Open-Meteo gives local times without a zone, e.g. "2026-10-10T18:45".
const localLayout = "2006-01-02T15:04"

func clock(t string) string {
	v, err := time.Parse(localLayout, t)
	if err != nil {
		return t
	}
	return v.Format("15:04")
}

func hourOf(t string) string {
	if len(t) >= 13 {
		return t[:13]
	}
	return t
}

// dayName names the i'th day of the forecast, which starts today.
func dayName(day string, i int) string {
	switch i {
	case 0:
		return "Today"
	case 1:
		return "Tomorrow"
	}
	v, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return v.Format("Monday")
}

// describe names a WMO weather code.
func describe(code int, day bool) string {
	switch code {
	case 0:
		if day {
			return "Sunny"
		}
		return "Clear"
	case 1:
		return "Mostly clear"
	case 2:
		return "Partly cloudy"
	case 3:
		return "Overcast"
	case 45, 48:
		return "Fog"
	case 51, 53, 55:
		return "Drizzle"
	case 56, 57:
		return "Freezing drizzle"
	case 61, 63:
		return "Rain"
	case 65:
		return "Heavy rain"
	case 66, 67:
		return "Freezing rain"
	case 71, 73, 75, 77:
		return "Snow"
	case 80, 81:
		return "Showers"
	case 82:
		return "Heavy showers"
	case 85, 86:
		return "Snow showers"
	case 95:
		return "Thunderstorms"
	case 96, 99:
		return "Thunderstorms with hail"
	}
	return "Code " + strconv.Itoa(code)
}
