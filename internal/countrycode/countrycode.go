// Package countrycode is the one place that decides what a country code is. The API speaks ISO 3166-1 alpha-2
// everywhere a country is meant (GB, not UK). "UK" is the hiring market's name for the United Kingdom, and it
// is the one alias callers may still send, so a client that has only seen market codes is not rejected.
package countrycode

import "strings"

// Normalize returns the ISO 3166-1 alpha-2 code for s: trimmed, upper case, with the "UK" alias turned into
// "GB". ok is false when the result is not an assigned ISO code.
func Normalize(s string) (code string, ok bool) {
	c := strings.ToUpper(strings.TrimSpace(s))
	if c == "UK" {
		c = "GB"
	}
	return c, Valid(c)
}

// Valid reports whether c is an officially assigned ISO 3166-1 alpha-2 code, in upper case.
func Valid(c string) bool { return assigned[c] }

var assigned = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields(`
AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ
BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ
CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ
DE DJ DK DM DO DZ
EC EE EG EH ER ES ET
FI FJ FK FM FO FR
GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY
HK HM HN HR HT HU
ID IE IL IM IN IO IQ IR IS IT
JE JM JO JP
KE KG KH KI KM KN KP KR KW KY KZ
LA LB LC LI LK LR LS LT LU LV LY
MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ
NA NC NE NF NG NI NL NO NP NR NU NZ
OM
PA PE PF PG PH PK PL PM PN PR PS PT PW PY
QA
RE RO RS RU RW
SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ
TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ
UA UG UM US UY UZ
VA VC VE VG VI VN VU
WF WS
YE YT
ZA ZM ZW`) {
		m[c] = true
	}
	return m
}()
