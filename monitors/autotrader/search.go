package autotrader

import (
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// filterNames maps search page URL parameters to the API's filter names.
var filterNames = map[string]string{
	"make":         "make",
	"model":        "model",
	"postcode":     "postcode",
	"transmission": "transmission",
	"minPrice":     "min_price",
	"price-from":   "min_price",
	"maxPrice":     "max_price",
	"price-to":     "max_price",
	"year-from":    "min_year_manufactured",
	"year-to":      "max_year_manufactured",
}

// ignoredParams are search page URL parameters that aren't filters.
var ignoredParams = map[string]bool{
	"channel": true,
	"flrfc":   true,
	"sort":    true,
	"page":    true,
}

type filter struct {
	Filter   string   `json:"filter"`
	Selected []string `json:"selected"`
}

type search struct {
	channel string
	make    string
	model   string
	filters []filter
	query   url.Values
}

// parseSearch turns an AutoTrader search page URL into API filters.
func parseSearch(raw string) (*search, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing search URL: %w", err)
	}
	q := u.Query()

	s := &search{
		channel: q.Get("channel"),
		make:    q.Get("make"),
		model:   q.Get("model"),
		// The API requires this; "total" means prices aren't monthly.
		filters: []filter{{Filter: "price_search_type", Selected: []string{"total"}}},
		query:   q,
	}
	if s.channel == "" {
		s.channel = "cars"
	}

	params := make([]string, 0, len(q))
	for p := range q {
		params = append(params, p)
	}
	sort.Strings(params)
	for _, p := range params {
		name, ok := filterNames[p]
		if !ok {
			if !ignoredParams[p] {
				slog.Warn("autotrader: ignoring unsupported search parameter", "param", p)
			}
			continue
		}
		s.filters = append(s.filters, filter{Filter: name, Selected: q[p]})
	}
	if s.make == "" {
		return nil, fmt.Errorf("search URL has no make")
	}
	return s, nil
}

// describe sums up the search's filters, e.g. "Mazda3 · Automatic · up to
// £14,000 · 2021 on". The postcode is left out.
func (s *search) describe() string {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := s.query.Get(k); v != "" {
				return v
			}
		}
		return ""
	}
	parts := []string{s.name()}
	if s.model != "" && !strings.Contains(strings.ToLower(s.model), strings.ToLower(s.make)) {
		parts[0] = s.make + " " + s.model
	}
	if t := get("transmission"); t != "" {
		parts = append(parts, t)
	}
	if r := span(pounds(get("minPrice", "price-from")), pounds(get("maxPrice", "price-to"))); r != "" {
		parts = append(parts, r)
	}
	if r := span(get("year-from"), get("year-to")); r != "" {
		parts = append(parts, r)
	}
	return strings.Join(parts, " · ")
}

// span describes a range with either end open: "2021 on", "up to £14,000",
// "2019–2022".
func span(from, to string) string {
	switch {
	case from != "" && to != "":
		return from + "–" + to
	case from != "":
		return from + " on"
	case to != "":
		return "up to " + to
	}
	return ""
}

// pounds writes a whole-pound price with thousands separators.
func pounds(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return ""
	}
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	return "£" + b.String()
}

// name describes what's being searched for, e.g. "Mazda3".
func (s *search) name() string {
	if s.model != "" {
		return s.model
	}
	return s.make
}
