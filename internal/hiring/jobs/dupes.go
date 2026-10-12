package jobs

import (
	"sort"
	"strings"
	"unicode"
)

// NormaliseTitle reduces a title to a comparison key: lower case, letters and digits only, single spaces.
// "Senior Engineer (Backend)" and "senior  engineer - backend" give the same key.
func NormaliseTitle(title string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(title) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		default:
			space = true
		}
	}
	return b.String()
}

// LocationKey is a comparison key for the set of places a job is open to, independent of order.
func LocationKey(mode string, sel []MarketSel) string {
	if mode == LocationAnywhere {
		return LocationAnywhere
	}
	parts := make([]string, 0, len(sel))
	for _, m := range sel {
		parts = append(parts, strings.Join([]string{
			strings.ToUpper(m.MarketCode), strings.ToUpper(m.Subdivision), strings.ToLower(m.City), m.Timezone,
		}, "|"))
	}
	sort.Strings(parts)
	return mode + ":" + strings.Join(parts, ";")
}

// DuplicateWindowDays is how long a closed job still counts as a duplicate.
const DuplicateWindowDays = 30

// Duplicate is an existing job that looks like the one being edited.
type Duplicate struct {
	ID      string `json:"id"`
	JobCode string `json:"job_code"`
	Title   string `json:"title"`
	Status  string `json:"status"`
}

// DuplicateWarning builds the warning shown for matching jobs. Only jobs the caller may see are passed in,
// so the warning never reveals a job the user cannot open.
func DuplicateWarning(dups []Duplicate) *Warning {
	if len(dups) == 0 {
		return nil
	}
	msg := "You already have a job with this title and location. Check that this is a different role."
	if len(dups) > 1 {
		msg = "You already have jobs with this title and location. Check that this is a different role."
	}
	return &Warning{Code: "duplicate_job", Severity: "warning", Title: "Possible duplicate", Message: msg}
}
