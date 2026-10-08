package jobs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Market is a launch market as stored in the global hiring.markets catalog.
type Market struct {
	Code               string    `json:"code"`
	Name               string    `json:"name"`
	IsOpen             bool      `json:"is_open"`
	MinRetentionMonths int       `json:"min_retention_months,omitempty"`
	Currencies         []string  `json:"currencies,omitempty"`
	Warnings           []Warning `json:"warnings"`
}

// MarketCatalog indexes markets by code.
type MarketCatalog map[string]Market

var subdivisionRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,10}$`)

const maxPlaceRunes = 80

// ValidateMarkets checks the places a job is open to against the catalog for the chosen location mode.
// It returns the cleaned selection (trimmed, upper-cased codes, duplicates removed) and the warnings of the
// markets the job will cover. For an "anywhere" job that is every open market, because the strictest rules
// apply (7 Oct decision).
func ValidateMarkets(mode string, sel []MarketSel, cat MarketCatalog) ([]MarketSel, []Warning, []FieldError) {
	var errs []FieldError
	if len(sel) > MaxMarkets {
		return nil, nil, []FieldError{errf("markets", "pick at most %d places", MaxMarkets)}
	}
	if mode == LocationAnywhere && len(sel) > 0 {
		errs = append(errs, errf("markets", "an anywhere job covers every open market; remove the places you picked"))
	}

	var clean []MarketSel
	seen := map[string]bool{}
	covered := map[string]bool{}
	for i, m := range sel {
		m, merrs := cleanMarket(fmt.Sprintf("markets[%d]", i), mode, m, cat)
		errs = append(errs, merrs...)
		if m == nil {
			continue
		}
		key := strings.Join([]string{m.MarketCode, m.Subdivision, strings.ToLower(m.City), m.Timezone}, "|")
		if seen[key] {
			continue
		}
		seen[key] = true
		covered[m.MarketCode] = true
		clean = append(clean, *m)
	}
	if mode == LocationAnywhere {
		for code, mk := range cat {
			if mk.IsOpen {
				covered[code] = true
			}
		}
	}
	return clean, marketWarnings(covered, cat), errs
}

// cleanMarket normalises one place and checks it. It returns nil for a market that cannot be used at all.
func cleanMarket(path, mode string, m MarketSel, cat MarketCatalog) (*MarketSel, []FieldError) {
	m.MarketCode = strings.ToUpper(trim(m.MarketCode))
	m.Subdivision, m.City, m.Timezone = strings.ToUpper(trim(m.Subdivision)), trim(m.City), trim(m.Timezone)

	mk, ok := cat[m.MarketCode]
	switch {
	case !ok:
		return nil, []FieldError{errf(path+".market_code", "unknown market")}
	case !mk.IsOpen:
		return nil, []FieldError{errf(path+".market_code", "%s is not open for hiring yet", mk.Name)}
	}
	var errs []FieldError
	if m.Subdivision != "" && !subdivisionRE.MatchString(m.Subdivision) {
		errs = append(errs, errf(path+".subdivision", "invalid state or region code"))
	}
	if utf8.RuneCountInString(m.City) > maxPlaceRunes {
		errs = append(errs, errf(path+".city", "city must be at most %d characters", maxPlaceRunes))
	}
	if m.Timezone != "" && !ValidTimezone(m.Timezone) {
		errs = append(errs, errf(path+".timezone", "unknown time zone"))
	}
	if mode == LocationSpecificTimezone && m.Timezone == "" {
		errs = append(errs, errf(path+".timezone", "a time zone job needs a time zone for each country"))
	}
	if mode == LocationSpecificArea && m.Timezone != "" {
		errs = append(errs, errf(path+".timezone", "time zones are only used when the hiring option is a time zone"))
	}
	return &m, errs
}

// marketWarnings lists the selection warnings of the covered markets, in market order.
func marketWarnings(covered map[string]bool, cat MarketCatalog) []Warning {
	codes := make([]string, 0, len(covered))
	for c := range covered {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	var warnings []Warning
	for _, c := range codes {
		for _, w := range cat[c].Warnings {
			w.Market = c
			warnings = append(warnings, w)
		}
	}
	return warnings
}

// MinRetention returns the longest minimum retention among the markets a job covers, or 0 for none.
func MinRetention(sel []MarketSel, mode string, cat MarketCatalog) int {
	best := 0
	consider := func(code string) {
		if m, ok := cat[code]; ok && m.MinRetentionMonths > best {
			best = m.MinRetentionMonths
		}
	}
	if mode == LocationAnywhere {
		for code, m := range cat {
			if m.IsOpen {
				consider(code)
			}
		}
		return best
	}
	for _, m := range sel {
		consider(m.MarketCode)
	}
	return best
}
