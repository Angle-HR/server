package kyb

import (
	"strings"
	"unicode"
)

// legalSuffixes are trailing company-form words ignored when comparing names,
// so "Acme Ltd" and "ACME LIMITED" match. Only trailing words are dropped.
var legalSuffixes = map[string]bool{
	"limited": true, "ltd": true, "plc": true, "llp": true, "lp": true,
	"inc": true, "incorporated": true, "llc": true, "corp": true, "corporation": true,
	"co": true, "company": true, "gmbh": true, "ag": true, "sa": true, "sarl": true,
	"bv": true, "nv": true, "ab": true, "as": true, "oy": true, "srl": true, "spa": true,
	"ug": true, "kg": true, "ou": true, "zoo": true, "sp": true,
}

// NormalizeName lower-cases a company name, turns "&" into "and", strips
// punctuation, collapses whitespace and removes a leading "the" and trailing
// legal-form words.
func NormalizeName(name string) string {
	name = strings.ToLower(strings.ReplaceAll(name, "&", " and "))
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	words := strings.Fields(b.String())
	if len(words) > 1 && words[0] == "the" {
		words = words[1:]
	}
	for len(words) > 1 && legalSuffixes[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, " ")
}

// NamesMatch reports whether two company names are the same legal name after normalisation.
func NamesMatch(a, b string) bool {
	na, nb := NormalizeName(a), NormalizeName(b)
	return na != "" && na == nb
}

// NormalizePostcode upper-cases a postcode and removes spaces and hyphens.
func NormalizePostcode(p string) string {
	p = strings.ToUpper(p)
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, p)
}

// AddressesMatch reports whether the submitted address plausibly describes the
// registered office. When both sides have a postcode it decides; otherwise at
// least half of the submitted first-line words must appear in the registry address.
// A mismatch is a soft warning, never a hard block (KYB V2 §5.2).
func AddressesMatch(submitted, registry *Address) bool {
	sp, rp := NormalizePostcode(submitted.PostCode), NormalizePostcode(registry.PostCode)
	if sp != "" && rp != "" {
		return sp == rp
	}
	sw := addressWords(submitted.Line1)
	if len(sw) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range addressWords(registry.Line1 + " " + registry.Line2 + " " + registry.City) {
		have[w] = true
	}
	hits := 0
	for _, w := range sw {
		if have[w] {
			hits++
		}
	}
	return hits*2 >= len(sw)
}

func addressWords(s string) []string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// FormatAddress renders an address on one line for display and storage.
func FormatAddress(a *Address) string {
	var parts []string
	for _, p := range []string{a.Line1, a.Line2, a.City, a.Region, a.PostCode, a.Country} {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ", ")
}
