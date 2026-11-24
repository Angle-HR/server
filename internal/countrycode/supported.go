package countrycode

import (
	"os"
	"strings"
)

// EnvSupported selects the countries employers may register in: "uk-eu" (the default: the United Kingdom and the
// 27 EU member states) or "any" (every ISO code, for tests and local experiments).
const EnvSupported = "SUPPORTED_COUNTRIES"

var euMembers = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(`AT BE BG HR CY CZ DK EE FI FR DE GR HU IE IT LV LT LU MT NL PL PT RO SK SI ES SE`) {
		m[c] = true
	}
	return m
}()

// IsUKOrEU reports whether c (upper-case ISO alpha-2) is the United Kingdom or an EU member state.
func IsUKOrEU(c string) bool { return c == "GB" || euMembers[c] }

// SupportedFromEnv returns the country filter selected by SUPPORTED_COUNTRIES; the default is United Kingdom and EU.
func SupportedFromEnv() func(code string) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvSupported)), "any") {
		return func(string) bool { return true }
	}
	return IsUKOrEU
}
