package onboarding

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// IdentificationField describes one business identification input for a country.
type IdentificationField struct {
	Key         string
	Label       string
	FormatHint  string
	Placeholder string
	Pattern     string
	Required    bool
}

// IdentificationRequirements is the field spec for a country.
type IdentificationRequirements struct {
	CountrySlug string
	Fields      []IdentificationField
}

var identificationBySlug = map[string]IdentificationRequirements{
	"united-kingdom": {
		CountrySlug: "united-kingdom",
		Fields: []IdentificationField{
			{
				Key:         "registration_number",
				Label:       "Company Registration Number (CRN)",
				FormatHint:  "8 digits, or SC/NI prefix + 6 digits",
				Placeholder: "12345678",
				Pattern:     `^(\d{8}|(SC|NI)\d{6})$`,
				Required:    true,
			},
		},
	},
	"nigeria": {
		CountrySlug: "nigeria",
		Fields: []IdentificationField{
			{
				Key:         "registration_number",
				Label:       "RC number",
				FormatHint:  "RC followed by 7 digits",
				Placeholder: "RC 1234567",
				Pattern:     `(?i)^(RC)?\s?\d{7}$`,
				Required:    true,
			},
		},
	},
	"germany": {
		CountrySlug: "germany",
		Fields: []IdentificationField{
			{
				Key:         "registration_number",
				Label:       "Handelsregisternummer",
				FormatHint:  "e.g. HRB 12345",
				Placeholder: "HRB 12345",
				Pattern:     `^HRB\s?\d+$`,
				Required:    true,
			},
		},
	},
	"united-states": {
		CountrySlug: "united-states",
		Fields: []IdentificationField{
			{
				Key:         "state_of_incorporation",
				Label:       "State of Incorporation",
				FormatHint:  "Two-letter US state code",
				Placeholder: "DE",
				Pattern:     `^[A-Z]{2}$`,
				Required:    true,
			},
			{
				Key:         "file_number",
				Label:       "State File Number",
				FormatHint:  "State-assigned file number",
				Placeholder: "1234567",
				Pattern:     `^.+$`,
				Required:    true,
			},
		},
	},
	"european-union": {
		CountrySlug: "european-union",
		Fields: []IdentificationField{
			{
				Key:         "vat_number",
				Label:       "VAT Number",
				FormatHint:  "Country prefix + digits",
				Placeholder: "DE123456789",
				Pattern:     `^[A-Z]{2}[A-Z0-9]{2,12}$`,
				Required:    true,
			},
		},
	},
	"india": {
		CountrySlug: "india",
		Fields: []IdentificationField{
			{
				Key:         "gstin",
				Label:       "GSTIN",
				FormatHint:  "15-character GSTIN",
				Placeholder: "27AAAAA0000A1Z5",
				Pattern:     `^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`,
				Required:    true,
			},
			{
				Key:         "cin",
				Label:       "CIN",
				FormatHint:  "21-character Corporate Identity Number",
				Placeholder: "U12345MH2020PTC123456",
				Pattern:     `^[A-Z0-9]{21}$`,
				Required:    true,
			},
		},
	},
	"kenya": {
		CountrySlug: "kenya",
		Fields: []IdentificationField{
			{
				Key:         "registration_number",
				Label:       "BRS Registration Number",
				FormatHint:  "PVT-XXXXXX or BN-XXXXXX",
				Placeholder: "PVT-ABC123",
				Pattern:     `^(PVT|BN)-[A-Z0-9]+$`,
				Required:    true,
			},
			{
				Key:         "kra_pin",
				Label:       "KRA PIN",
				FormatHint:  "Kenya Revenue Authority PIN",
				Placeholder: "A123456789X",
				Pattern:     `^[A-Z]\d{9}[A-Z]$`,
				Required:    true,
			},
		},
	},
}

// IdentificationRequirementsForCountry returns requirements for a country slug.
func IdentificationRequirementsForCountry(countrySlug string) (IdentificationRequirements, bool) {
	req, ok := identificationBySlug[countrySlug]
	return req, ok
}

// ValidateIdentification checks submitted identification values against country rules.
func ValidateIdentification(countrySlug string, values map[string]string) error {
	req, ok := IdentificationRequirementsForCountry(countrySlug)
	if !ok {
		return fmt.Errorf("identification requirements not configured for country %q", countrySlug)
	}

	for _, field := range req.Fields {
		value := strings.TrimSpace(values[field.Key])
		if field.Required && value == "" {
			return fmt.Errorf("%s is required", field.Key)
		}
		if value == "" {
			continue
		}
		re, err := regexp.Compile(field.Pattern)
		if err != nil {
			return fmt.Errorf("invalid pattern for %s", field.Key)
		}
		matched := re.MatchString(value)
		// #region agent log
		if countrySlug == "nigeria" && field.Key == "registration_number" {
			debugLogIdentification("identification_requirements.go:ValidateIdentification", "nigeria registration_number validation", map[string]any{
				"hypothesisId": "A",
				"value":        value,
				"pattern":      field.Pattern,
				"formatHint":   field.FormatHint,
				"matched":      matched,
			})
		}
		// #endregion
		if !matched {
			return fmt.Errorf("%s has invalid format", field.Key)
		}
	}

	return nil
}

// PrimaryIdentificationNumber returns the main registry number for storage in bin_number.
func PrimaryIdentificationNumber(countrySlug string, values map[string]string) string {
	switch countrySlug {
	case "united-states":
		state := strings.TrimSpace(values["state_of_incorporation"])
		file := strings.TrimSpace(values["file_number"])
		if state == "" && file == "" {
			return ""
		}
		return state + ":" + file
	case "india":
		if v := strings.TrimSpace(values["gstin"]); v != "" {
			return v
		}
		return strings.TrimSpace(values["cin"])
	case "european-union":
		return strings.TrimSpace(values["vat_number"])
	case "nigeria":
		raw := strings.TrimSpace(values["registration_number"])
		re := regexp.MustCompile(`(?i)^(?:RC\s?)?(\d{7})$`)
		if m := re.FindStringSubmatch(raw); len(m) == 2 {
			normalized := m[1]
			// #region agent log
			debugLogIdentification("identification_requirements.go:PrimaryIdentificationNumber", "nigeria bin_number normalized", map[string]any{
				"hypothesisId": "C",
				"raw":          raw,
				"normalized":   normalized,
			})
			// #endregion
			return normalized
		}
		return raw
	default:
		return strings.TrimSpace(values["registration_number"])
	}
}

// #region agent log
func debugLogIdentification(location, message string, data map[string]any) {
	payload := map[string]any{
		"sessionId":    "b69c38",
		"location":     location,
		"message":      message,
		"data":         data,
		"timestamp":    time.Now().UnixMilli(),
		"runId":        "pre-fix",
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return
	}
	f, err := os.OpenFile("/Users/mac/server/.cursor/debug-b69c38.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// #endregion
