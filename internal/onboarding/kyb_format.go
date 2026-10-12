package onboarding

import (
	"regexp"
	"strings"

	"github.com/Angle-HR/server/internal/kyb"
)

// registrationSlugByCountry maps an ISO country code to the identification
// requirements slug for countries whose registry number is a single
// "registration_number" field. Other countries (US, India, EU) use several
// identifiers, so the format check passes them and the verifier decides.
var registrationSlugByCountry = map[string]string{
	"GB": "united-kingdom",
	"NG": "nigeria",
	"DE": "germany",
	"KE": "kenya",
}

// RegistrationNumberFormatOK reports whether a registration number has the right
// shape for the country. It is the kyb.FormatChecker: a number that is clearly
// from another country fails verification as wrong_country. Countries without a
// single-number rule always pass.
func RegistrationNumberFormatOK(countryCode, registrationNumber string) bool {
	code := strings.ToUpper(strings.TrimSpace(countryCode))
	slug, ok := registrationSlugByCountry[code]
	if !ok {
		return true
	}
	number := strings.TrimSpace(registrationNumber)
	if code == "GB" {
		number = kyb.NormalizeUKNumber(number)
	}
	req, ok := IdentificationRequirementsForCountry(slug)
	if !ok {
		return true
	}
	// Only the registration number is checked: other fields (such as the Kenyan
	// KRA PIN) are not part of the KYB form.
	for _, field := range req.Fields {
		if field.Key != "registration_number" {
			continue
		}
		re, err := regexp.Compile(field.Pattern)
		return err == nil && re.MatchString(number)
	}
	return true
}
