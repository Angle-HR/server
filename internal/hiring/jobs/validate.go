package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Limits that keep a draft small and well under the API body cap.
const (
	MaxTitleRunes       = 70
	MaxSkills           = 30
	MaxSkillLabelRunes  = 60
	MaxMarkets          = 30
	MaxDescriptionBytes = 200 * 1024
	maxLocationText     = 200
	maxCustomIndustry   = 80
	maxURLLength        = 500
	maxPayMinor         = int64(1_000_000_000_000)
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s looks like a UUID.
func IsUUID(s string) bool { return uuidRE.MatchString(s) }

// titleAllowed lists the punctuation a title may contain besides letters, digits and spaces.
const titleAllowed = "-/&(),.+#'’"

// ValidTitleChars reports whether every rune of s is a letter, digit, space or allowed punctuation.
func ValidTitleChars(s string) bool {
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && !strings.ContainsRune(titleAllowed, r) {
			return false
		}
	}
	return true
}

// Check validates the format of every value that is set. Empty values pass: use Required for completeness.
// now is the reference time for the closing date rule.
func Check(d *Details, now time.Time) []FieldError {
	var errs []FieldError
	errs = append(errs, checkTitle(d.Title)...)
	if d.DepartmentID != "" && !IsUUID(d.DepartmentID) {
		errs = append(errs, errf("department_id", "invalid department"))
	}
	errs = append(errs, checkClosingDate(d.ClosingDate, now)...)
	checkEnum(&errs, "location_mode", d.LocationMode, LocationModes)
	checkEnum(&errs, "workplace_type", d.WorkplaceType, WorkplaceTypes)
	checkEnum(&errs, "travel_frequency", d.TravelFrequency, TravelFrequencies)
	checkEnum(&errs, "visa_sponsorship", d.VisaSponsorship, VisaPolicies)
	checkEnum(&errs, "employment_type", d.EmploymentType, EmploymentTypes)
	checkEnum(&errs, "lawful_basis", d.LawfulBasis, LawfulBases)
	errs = append(errs, checkIndustry(d)...)
	errs = append(errs, checkDataProtection(d)...)
	if utf8.RuneCountInString(d.LocationText) > maxLocationText {
		errs = append(errs, errf("location_text", "location text must be at most %d characters", maxLocationText))
	}
	errs = append(errs, checkDescription(d.Description)...)
	errs = append(errs, checkSkills(d.Skills)...)
	errs = append(errs, checkPay(&d.Pay)...)
	return errs
}

func checkTitle(title string) []FieldError {
	t := trim(title)
	if t == "" {
		return nil
	}
	var errs []FieldError
	if utf8.RuneCountInString(t) > MaxTitleRunes {
		errs = append(errs, errf("title", "title must be at most %d characters", MaxTitleRunes))
	}
	if !ValidTitleChars(t) {
		errs = append(errs, errf("title", "title can only contain letters, numbers, spaces and - / & ( ) , . + # '"))
	}
	return errs
}

func checkClosingDate(value string, now time.Time) []FieldError {
	if value == "" {
		return nil
	}
	cd, err := time.Parse("2006-01-02", value)
	switch {
	case err != nil:
		return []FieldError{errf("closing_date", "closing date must be a date as YYYY-MM-DD")}
	case cd.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)):
		return []FieldError{errf("closing_date", "closing date cannot be in the past")}
	}
	return nil
}

// checkIndustry covers the catalog references and the custom industry text.
func checkIndustry(d *Details) []FieldError {
	var errs []FieldError
	if utf8.RuneCountInString(d.CustomIndustry) > maxCustomIndustry {
		errs = append(errs, errf("custom_industry", "industry must be at most %d characters", maxCustomIndustry))
	}
	refs := []struct{ path, id string }{
		{"industry_id", d.IndustryID}, {"seniority_level_id", d.SeniorityLevelID},
		{"experience_range_id", d.ExperienceRangeID},
	}
	for _, ref := range refs {
		if ref.id != "" && !IsUUID(ref.id) {
			errs = append(errs, errf(ref.path, "invalid reference"))
		}
	}
	if d.IndustryID != "" && d.CustomIndustry != "" {
		errs = append(errs, errf("custom_industry", "pick an industry or enter your own, not both"))
	}
	return errs
}

// checkDataProtection covers retention, the legitimate interests reference and the assessment link.
func checkDataProtection(d *Details) []FieldError {
	var errs []FieldError
	if d.RetentionMonths != 0 && !RetentionOptions[d.RetentionMonths] {
		errs = append(errs, errf("retention_months", "retention must be 3, 6 or 12 months"))
	}
	if len(d.LIAReference) > maxURLLength {
		errs = append(errs, errf("lia_reference", "reference is too long"))
	}
	if d.AssessmentURL != "" && !validHTTPS(d.AssessmentURL) {
		errs = append(errs, errf("assessment_url", "assessment link must be an https:// URL"))
	}
	return errs
}

func validHTTPS(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && len(raw) <= maxURLLength
}

func checkEnum(errs *[]FieldError, path, value string, allowed map[string]bool) {
	if value != "" && !allowed[value] {
		*errs = append(*errs, errf(path, "%q is not a valid value", value))
	}
}

func checkDescription(s Sections) []FieldError {
	var errs []FieldError
	known := set(DescriptionKeys...)
	total := 0
	for k, v := range s {
		path := "description_sections." + k
		if !known[k] {
			errs = append(errs, errf(path, "unknown description section"))
			continue
		}
		if !json.Valid(v) {
			errs = append(errs, errf(path, "section must be valid JSON"))
		}
		total += len(v)
	}
	if total > MaxDescriptionBytes {
		errs = append(errs, errf("description_sections", "description is too long"))
	}
	return errs
}

func checkSkills(skills []Skill) []FieldError {
	var errs []FieldError
	if len(skills) > MaxSkills {
		errs = append(errs, errf("skills", "a job can list at most %d skills", MaxSkills))
	}
	seen := map[string]bool{}
	for i, sk := range skills {
		path := fmt.Sprintf("skills[%d]", i)
		label := trim(sk.CustomLabel)
		switch {
		case (sk.SkillID == "") == (label == ""):
			errs = append(errs, errf(path, "give either skill_id or custom_label"))
			continue
		case sk.SkillID != "" && !IsUUID(sk.SkillID):
			errs = append(errs, errf(path+".skill_id", "invalid skill"))
			continue
		case utf8.RuneCountInString(label) > MaxSkillLabelRunes:
			errs = append(errs, errf(path+".custom_label", "skill must be at most %d characters", MaxSkillLabelRunes))
			continue
		}
		key := strings.ToLower(sk.SkillID + "|" + label)
		if seen[key] {
			errs = append(errs, errf(path, "skill is listed twice"))
		}
		seen[key] = true
	}
	return errs
}

func checkPay(p *Pay) []FieldError {
	var errs []FieldError
	checkEnum(&errs, "pay.type", p.Type, PayTypes)
	checkEnum(&errs, "pay.period", p.Period, PayPeriods)
	if p.Currency != "" && !Currencies[p.Currency] {
		errs = append(errs, errf("pay.currency", "%q is not a supported currency", p.Currency))
	}
	for path, v := range map[string]*int64{"pay.min": p.Min, "pay.max": p.Max} {
		if v != nil && (*v < 0 || *v > maxPayMinor) {
			errs = append(errs, errf(path, "amount is out of range"))
		}
	}
	if p.Min != nil && p.Max != nil && *p.Min > *p.Max {
		errs = append(errs, errf("pay.max", "maximum must not be below the minimum"))
	}
	return errs
}

// Options tune the completeness check.
type Options struct {
	// RequirePay is set when the caller may set pay. Roles without job.salary_range.set leave pay to
	// someone who can, so their drafts are complete without it.
	RequirePay bool
}

// Required reports what is missing for "Save & continue" on the job details step.
// The required fields are the ones the Figma error states mark: title, department, hiring option,
// travel frequency, visa policy, employment type and pay type.
func Required(d *Details, opts Options) []FieldError {
	var errs []FieldError
	need := func(path, value, name string) {
		if trim(value) == "" {
			errs = append(errs, errf(path, "%s is required", name))
		}
	}
	need("title", d.Title, "title")
	need("department_id", d.DepartmentID, "department")
	need("location_mode", d.LocationMode, "hiring option")
	need("travel_frequency", d.TravelFrequency, "travel frequency")
	need("visa_sponsorship", d.VisaSponsorship, "visa policy")
	need("employment_type", d.EmploymentType, "employment type")

	switch d.LocationMode {
	case LocationSpecificArea:
		if len(d.Markets) == 0 {
			errs = append(errs, errf("markets", "pick at least one place you are hiring in"))
		}
	case LocationSpecificTimezone:
		if !hasTimezone(d.Markets) {
			errs = append(errs, errf("markets", "pick at least one time zone"))
		}
	}
	if opts.RequirePay {
		errs = append(errs, requiredPay(&d.Pay)...)
	}
	return errs
}

func hasTimezone(sel []MarketSel) bool {
	for _, m := range sel {
		if m.Timezone != "" {
			return true
		}
	}
	return false
}

func requiredPay(p *Pay) []FieldError {
	var errs []FieldError
	if p.Type == "" {
		return []FieldError{errf("pay.type", "pay type is required")}
	}
	if p.Currency == "" {
		errs = append(errs, errf("pay.currency", "currency is required"))
	}
	if p.Period == "" {
		errs = append(errs, errf("pay.period", "pay period is required"))
	}
	if p.Min == nil {
		errs = append(errs, errf("pay.min", "amount is required"))
	}
	if p.Type == "range" && p.Max == nil {
		errs = append(errs, errf("pay.max", "maximum is required for a range"))
	}
	return errs
}

// Warnings returns advice about the details that does not block saving.
func Warnings(d *Details) []Warning {
	var out []Warning
	p := d.Pay
	if p.Complete() && p.Type == "range" {
		if *p.Min == 0 {
			out = append(out, Warning{Code: "pay_min_zero", Severity: "warning",
				Message: "The minimum pay is 0. Check the range before you publish."})
		}
		if *p.Min > 0 && *p.Max > *p.Min*2 {
			out = append(out, Warning{Code: "pay_range_wide", Severity: "warning",
				Message: "The maximum is more than double the minimum. A narrower range reads as more credible."})
		}
	}
	if d.LawfulBasis == "consent" {
		out = append(out, Warning{Code: "lawful_basis_consent", Severity: "warning",
			Message: "Consent for hiring is hard to rely on because candidates can withdraw it. " +
				"Most employers use legitimate interests or contract."})
	}
	return out
}

// Completed returns the data sections the details complete, in a stable order.
func Completed(d *Details) []string {
	var out []string
	if basicsComplete(d) {
		out = append(out, SectionBasics)
	}
	if marketsComplete(d) {
		out = append(out, SectionMarkets)
	}
	if requiredPayOK(&d.Pay) {
		out = append(out, SectionCompensation)
	}
	if d.LawfulBasis != "" {
		out = append(out, SectionDataProtection)
	}
	return out
}

func requiredPayOK(p *Pay) bool { return len(requiredPay(p)) == 0 }

func basicsComplete(d *Details) bool {
	for _, v := range []string{d.Title, d.DepartmentID, d.TravelFrequency, d.VisaSponsorship, d.EmploymentType} {
		if trim(v) == "" {
			return false
		}
	}
	return true
}

func marketsComplete(d *Details) bool {
	switch d.LocationMode {
	case LocationAnywhere:
		return true
	case LocationSpecificArea:
		return len(d.Markets) > 0
	case LocationSpecificTimezone:
		return hasTimezone(d.Markets)
	}
	return false
}

// NextStep returns the wizard step a draft should resume at given its completed sections and whether the
// application form has been saved.
func NextStep(completed []string, formSaved bool) string {
	has := set(completed...)
	if !has[SectionBasics] || !has[SectionMarkets] {
		return StepDetails
	}
	if !formSaved {
		return StepApplicationForm
	}
	return StepPermissions
}

// MergeSections returns the union of a and b, keeping a's order first.
func MergeSections(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range append(append([]string(nil), a...), b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// PlainText extracts the visible text of the structured description for search and email.
// The editor's JSON is the source of truth; this is only a derived copy.
func PlainText(s Sections) string {
	var parts []string
	for _, k := range DescriptionKeys {
		raw, ok := s[k]
		if !ok {
			continue
		}
		var node any
		if err := json.Unmarshal(raw, &node); err != nil {
			continue
		}
		var b strings.Builder
		walkText(node, &b)
		if t := strings.TrimSpace(b.String()); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

// walkText collects every "text" string in an editor document (and bare strings).
func walkText(n any, b *strings.Builder) {
	switch v := n.(type) {
	case string:
		b.WriteString(v)
		b.WriteByte(' ')
	case []any:
		for _, c := range v {
			walkText(c, b)
		}
	case map[string]any:
		if t, ok := v["text"].(string); ok {
			b.WriteString(t)
			b.WriteByte(' ')
		}
		if c, ok := v["content"]; ok {
			walkText(c, b)
		}
		if c, ok := v["children"]; ok {
			walkText(c, b)
		}
	}
}

// SectionsEqual reports whether two descriptions hold the same JSON.
func SectionsEqual(a, b Sections) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !bytes.Equal(compact(av), compact(bv)) {
			return false
		}
	}
	return true
}

func compact(raw json.RawMessage) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
