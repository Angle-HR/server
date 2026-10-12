package questions

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// FormQuestion is a question as the form builder sends and receives it.
type FormQuestion struct {
	ID              string          `json:"id,omitempty"`
	Section         string          `json:"section"`
	Position        int             `json:"position"`
	Type            string          `json:"type"`
	Label           string          `json:"label"`
	Description     string          `json:"description,omitempty"`
	HelperText      string          `json:"helper_text,omitempty"`
	Required        bool            `json:"required"`
	Config          json.RawMessage `json:"config"`
	SystemKey       string          `json:"system_key,omitempty"`
	Locked          bool            `json:"locked"` // set by the server: full name and email cannot be removed or made optional
	Knockout        json.RawMessage `json:"knockout,omitempty"`
	SpecialCategory string          `json:"special_category,omitempty"`
}

// Declaration records the legal condition for collecting one special category of data.
type Declaration struct {
	Category       string `json:"category"`
	LegalCondition string `json:"legal_condition"`
	Purpose        string `json:"purpose"`
}

// Policy carries the company settings that change what a form may contain.
type Policy struct {
	// AllowKnockout is true once Legal has enabled automated screening for the company.
	AllowKnockout bool
	// NewID makes ids for options that do not have one. Nil uses random ids.
	NewID func() string
}

// Limits on text a builder can enter.
const (
	maxLabelRunes  = 300
	maxDescRunes   = 1000
	maxHelperRunes = 300
	maxPurposeRune = 300
)

// Legal conditions a special category can be collected under. Art. 9 conditions apply to every category
// except criminal records, which fall under Art. 10. Wording is for counsel to confirm.
var (
	Art9Conditions = []string{
		"explicit_consent", "employment_law", "legal_claims", "substantial_public_interest", "occupational_medicine",
	}
	Art10Conditions = []string{"law_authorised", "official_authority"}
)

func conditionAllowed(category, condition string) bool {
	list := Art9Conditions
	if category == "criminal_records" {
		list = Art10Conditions
	}
	for _, c := range list {
		if c == condition {
			return true
		}
	}
	return false
}

// systemFields lists the fixed fields and the question type each one must use.
var systemFields = map[string]string{
	"full_name":       "short_text",
	"email":           "email",
	"location":        "short_text",
	"cv":              "file_upload",
	"autofill_resume": "autofill_resume",
}

// lockedFields cannot be removed or made optional.
var lockedFields = map[string]bool{"full_name": true, "email": true}

// randomIDBytes is how many random bytes make an option id.
const randomIDBytes = 6

func randomID() string {
	b := make([]byte, randomIDBytes)
	if _, err := rand.Read(b); err != nil {
		panic("questions: no randomness available: " + err.Error())
	}
	return "o_" + hex.EncodeToString(b)
}

// Build normalises and validates a submitted form. It returns the questions in saved order (positions
// renumbered from zero, option ids filled in, locked flags set) together with the cleaned declarations.
// Every problem is returned, each with a path such as questions[3].config.
func Build(in []FormQuestion, decls []Declaration, pol Policy) ([]FormQuestion, []Declaration, []FieldError) {
	if pol.NewID == nil {
		pol.NewID = randomID
	}
	var errs []FieldError

	cleanDecls, declared, declErrs := validateDeclarations(decls)
	errs = append(errs, declErrs...)

	out := make([]FormQuestion, len(in))
	for i := range in {
		q := in[i]
		q.Label = strings.TrimSpace(q.Label)
		q.Description = strings.TrimSpace(q.Description)
		q.HelperText = strings.TrimSpace(q.HelperText)
		q.Position = i
		q.Locked = lockedFields[q.SystemKey]
		if len(strings.TrimSpace(string(q.Config))) == 0 || string(q.Config) == "null" {
			q.Config = json.RawMessage(`{}`)
		}
		if cfg, err := assignOptionIDs(q.Type, q.Config, pol.NewID); err != nil {
			errs = append(errs, pathErr(i, "config", err.Error()))
		} else {
			q.Config = cfg
		}
		out[i] = q
		errs = append(errs, checkQuestion(i, &q, pol)...)
	}

	base := make([]Question, len(out))
	for i, q := range out {
		base[i] = Question{
			Section: q.Section, Type: q.Type, Label: q.Label, Required: q.Required, Config: q.Config,
			SystemKey: q.SystemKey, SpecialCategory: q.SpecialCategory,
		}
	}
	errs = append(errs, dedupe(ValidateForm(base, declared))...)
	errs = append(errs, duplicateSystemKeys(out)...)
	return out, cleanDecls, dedupeErrors(errs)
}

func pathErr(i int, field, msg string) FieldError {
	return FieldError{Path: fmt.Sprintf("questions[%d].%s", i, field), Message: msg}
}

// checkQuestion runs the builder rules that ValidateForm does not cover.
func checkQuestion(i int, q *FormQuestion, pol Policy) []FieldError {
	errs := checkLengths(i, q)
	if q.SystemKey != "" {
		errs = append(errs, checkSystemField(i, q)...)
	}
	if mentionsPayHistory(q) {
		errs = append(errs, pathErr(i, "label",
			"questions about current or past pay are not allowed; ask about salary expectations instead"))
	}
	if len(q.Knockout) > 0 && string(q.Knockout) != "null" {
		errs = append(errs, checkKnockout(i, q, pol)...)
	}
	return errs
}

func checkLengths(i int, q *FormQuestion) []FieldError {
	var errs []FieldError
	if utf8.RuneCountInString(q.Label) > maxLabelRunes {
		errs = append(errs, pathErr(i, "label", fmt.Sprintf("label must be at most %d characters", maxLabelRunes)))
	}
	if utf8.RuneCountInString(q.Description) > maxDescRunes {
		errs = append(errs, pathErr(i, "description",
			fmt.Sprintf("description must be at most %d characters", maxDescRunes)))
	}
	if utf8.RuneCountInString(q.HelperText) > maxHelperRunes {
		errs = append(errs, pathErr(i, "helper_text",
			fmt.Sprintf("helper text must be at most %d characters", maxHelperRunes)))
	}
	return errs
}

// checkSystemField covers the fixed fields: name, email, location, CV and resume autofill.
func checkSystemField(i int, q *FormQuestion) []FieldError {
	var errs []FieldError
	add := func(field, msg string) { errs = append(errs, pathErr(i, field, msg)) }
	want, ok := systemFields[q.SystemKey]
	switch {
	case !ok:
		add("system_key", "unknown system field")
	case q.Type != want:
		add("type", fmt.Sprintf("the %s field must be a %s question", q.SystemKey, want))
	}
	if (q.SystemKey == "full_name" || q.SystemKey == "email") && q.Section != "personal_information" {
		add("section", fmt.Sprintf("the %s field belongs in personal_information", q.SystemKey))
	}
	if q.SystemKey == "cv" {
		errs = append(errs, checkCVConfig(i, q.Config)...)
	}
	if q.SpecialCategory != "" || len(q.Knockout) > 0 {
		add("system_key", "system fields cannot be special category or knockout questions")
	}
	return errs
}

func checkKnockout(i int, q *FormQuestion, pol Policy) []FieldError {
	if !pol.AllowKnockout {
		return []FieldError{pathErr(i, "knockout",
			"knockout questions need automated screening, which Legal must enable for your company first")}
	}
	if msg := validateKnockout(q); msg != "" {
		return []FieldError{pathErr(i, "knockout", msg)}
	}
	return nil
}

func checkCVConfig(i int, cfg json.RawMessage) []FieldError {
	var c uploadConfig
	if err := decodeStrict(cfg, &c); err != nil {
		return nil // reported by ValidateConfig
	}
	for _, a := range c.Accept {
		if !cvFormats[strings.ToLower(a)] {
			return []FieldError{pathErr(i, "config", "CV uploads accept PDF only")}
		}
	}
	return nil
}

func duplicateSystemKeys(qs []FormQuestion) []FieldError {
	var errs []FieldError
	seen := map[string]int{}
	for i, q := range qs {
		if q.SystemKey == "" {
			continue
		}
		if first, dup := seen[q.SystemKey]; dup {
			msg := fmt.Sprintf("the %s field is already at position %d", q.SystemKey, first)
			errs = append(errs, pathErr(i, "system_key", msg))
			continue
		}
		seen[q.SystemKey] = i
	}
	return errs
}

// ---- option ids ----

var choiceTypes = map[string]bool{"single_choice": true, "multiple_choice": true, "checkbox": true, "dropdown": true}

// optionID reads the id of an option; a missing or malformed id counts as none.
func optionID(o map[string]json.RawMessage) string {
	var id string
	if err := json.Unmarshal(o["id"], &id); err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// assignOptionIDs gives every option without an id a new stable one. Renaming an option later keeps its id,
// so reports and answers survive edits.
func assignOptionIDs(typ string, cfg json.RawMessage, newID func() string) (json.RawMessage, error) {
	if !choiceTypes[typ] {
		return cfg, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &m); err != nil {
		return cfg, fmt.Errorf("invalid config: %w", err)
	}
	rawOpts, ok := m["options"]
	if !ok {
		return cfg, nil
	}
	var opts []map[string]json.RawMessage
	if err := json.Unmarshal(rawOpts, &opts); err != nil {
		return cfg, errors.New("invalid config: options must be a list")
	}
	used := map[string]bool{}
	for _, o := range opts {
		if id := optionID(o); id != "" {
			used[id] = true
		}
	}
	changed := false
	for _, o := range opts {
		if optionID(o) != "" {
			continue
		}
		fresh := newID()
		for used[fresh] {
			fresh = newID()
		}
		used[fresh] = true
		encoded, err := json.Marshal(fresh)
		if err != nil {
			return cfg, err
		}
		o["id"] = encoded
		changed = true
	}
	if !changed {
		return cfg, nil
	}
	rawOpts, err := json.Marshal(opts)
	if err != nil {
		return cfg, err
	}
	m["options"] = rawOpts
	return json.Marshal(m)
}

// ---- pay history ----

var payHistoryRE = []*regexp.Regexp{
	regexp.MustCompile(`\b(current|currently|present|previous|prior|past|last|former|existing)\b[^.?!]{0,25}` +
		`\b(salary|salaries|pay|wage|wages|earnings|compensation|ctc|package|remuneration)\b`),
	regexp.MustCompile(`\b(salary|pay|wage|compensation|earnings|ctc)\s+(history|details|slip|slips|stub|stubs)\b`),
	regexp.MustCompile(`\bhow much\b[^.?!]{0,20}` +
		`\b(earn|earned|earning|make|made|paid|get paid|are you paid|were you paid)\b`),
	regexp.MustCompile(`\bwhat\b[^.?!]{0,15}\b(did|do|were|are)\b[^.?!]{0,15}\b(earn|earning|paid|making)\b`),
}

// expectationWindow is how many bytes after a pay word are searched for an expectation word.
const expectationWindow = 30

// payExpectation words mark a question about what the candidate wants, which is allowed.
var payExpectationRE = regexp.MustCompile(`\b(expect|expected|expectation|expectations|desired|target|` +
	`looking for|aspiration|minimum|require|required|requirement)\b`)

// mentionsPayHistory reports whether a question asks what the candidate earns or earned. Asking for salary
// expectations is allowed. The ban applies in every market (EU Pay Transparency Directive Art. 5(2) and US
// salary history laws).
func mentionsPayHistory(q *FormQuestion) bool {
	texts := []string{q.Label, q.Description, q.HelperText}
	if choiceTypes[q.Type] {
		var c choiceConfig
		if json.Unmarshal(q.Config, &c) == nil {
			for _, o := range c.Options {
				texts = append(texts, o.Label)
			}
		}
	}
	for _, t := range texts {
		if PayHistoryText(t) {
			return true
		}
	}
	return false
}

// PayHistoryText reports whether text asks about current or past pay.
func PayHistoryText(text string) bool {
	t := strings.ToLower(text)
	if strings.TrimSpace(t) == "" {
		return false
	}
	for _, re := range payHistoryRE {
		loc := re.FindStringIndex(t)
		if loc == nil {
			continue
		}
		// "current salary expectations" is about the future: allowed.
		tail := t[loc[1]:]
		if len(tail) > expectationWindow {
			tail = tail[:expectationWindow]
		}
		if payExpectationRE.MatchString(tail) {
			continue
		}
		return true
	}
	return false
}

// ---- knockout ----

type knockoutChoice struct {
	OptionIDs []string `json:"option_ids"`
	Match     string   `json:"match"`
	Reason    string   `json:"reason"`
}

type knockoutNumber struct {
	Operator string   `json:"operator"`
	Value    *float64 `json:"value"`
	Reason   string   `json:"reason"`
}

var numberOperators = map[string]bool{"lt": true, "lte": true, "gt": true, "gte": true, "eq": true, "neq": true}

// validateKnockout checks the rule that flags a candidate for human review. It returns "" when valid.
// A knockout never rejects anyone: a failed rule opens a review task.
func validateKnockout(q *FormQuestion) string {
	switch q.Type {
	case "single_choice", "dropdown", "multiple_choice", "checkbox":
		return validateChoiceKnockout(q)
	case "number", "linear_scale":
		return validateNumberKnockout(q)
	}
	return "knockout is not available for this question type"
}

func validateChoiceKnockout(q *FormQuestion) string {
	var k knockoutChoice
	if err := decodeStrict(q.Knockout, &k); err != nil {
		return "knockout must be {option_ids, match?, reason?}"
	}
	var c choiceConfig
	if err := json.Unmarshal(q.Config, &c); err != nil {
		return "question options are invalid"
	}
	if len(k.OptionIDs) == 0 {
		return "pick at least one answer that flags the candidate"
	}
	valid := optionSet(c.Options)
	for _, id := range k.OptionIDs {
		if !valid[id] {
			return "knockout refers to an option that does not exist"
		}
	}
	multi := q.Type == "multiple_choice" || q.Type == "checkbox"
	switch k.Match {
	case "":
	case "any", "all":
		if !multi {
			return "match is only used for questions that allow several answers"
		}
	default:
		return "match must be any or all"
	}
	if utf8.RuneCountInString(k.Reason) > maxPurposeRune {
		return "reason is too long"
	}
	return ""
}

func validateNumberKnockout(q *FormQuestion) string {
	var k knockoutNumber
	if err := decodeStrict(q.Knockout, &k); err != nil {
		return "knockout must be {operator, value, reason?}"
	}
	if !numberOperators[k.Operator] || k.Value == nil {
		return "knockout needs an operator (lt, lte, gt, gte, eq, neq) and a value"
	}
	if utf8.RuneCountInString(k.Reason) > maxPurposeRune {
		return "reason is too long"
	}
	return ""
}

// ---- declarations ----

func validateDeclarations(in []Declaration) ([]Declaration, map[string]bool, []FieldError) {
	var errs []FieldError
	declared := map[string]bool{}
	var out []Declaration
	for i, d := range in {
		path := func(f string) string { return fmt.Sprintf("special_categories[%d].%s", i, f) }
		d.Category = strings.TrimSpace(d.Category)
		d.LegalCondition = strings.TrimSpace(d.LegalCondition)
		d.Purpose = strings.TrimSpace(d.Purpose)
		switch {
		case !SpecialCategories[d.Category]:
			errs = append(errs, FieldError{Path: path("category"), Message: "unknown special category"})
			continue
		case declared[d.Category]:
			errs = append(errs, FieldError{Path: path("category"), Message: "category is declared twice"})
			continue
		}
		if !conditionAllowed(d.Category, d.LegalCondition) {
			msg := "pick one of the Art. 9 conditions"
			if d.Category == "criminal_records" {
				msg = "criminal records data needs an Art. 10 condition"
			}
			errs = append(errs, FieldError{Path: path("legal_condition"), Message: msg})
		}
		if d.Purpose == "" || utf8.RuneCountInString(d.Purpose) > maxPurposeRune {
			errs = append(errs, FieldError{Path: path("purpose"), Message: "describe the purpose in up to 300 characters"})
		}
		declared[d.Category] = true
		out = append(out, d)
	}
	return out, declared, errs
}

// ---- helpers ----

func dedupe(errs []FieldError) []FieldError { return dedupeErrors(errs) }

func dedupeErrors(errs []FieldError) []FieldError {
	seen := map[FieldError]bool{}
	out := errs[:0:0]
	for _, e := range errs {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

// DefaultForm is the form every new job starts with: the locked name and email, phone and location,
// the CV upload and resume autofill.
func DefaultForm() []FormQuestion {
	empty := json.RawMessage(`{}`)
	upload := json.RawMessage(`{"max_mb":5,"accept":["pdf"]}`)
	const personal, profile = "personal_information", "profile"
	return []FormQuestion{
		{Section: personal, Type: "short_text", Label: "Full name", Required: true, SystemKey: "full_name", Config: empty},
		{Section: personal, Type: "email", Label: "Email", Required: true, SystemKey: "email", Config: empty},
		{Section: personal, Type: "phone_number", Label: "Phone number", Config: empty},
		{Section: personal, Type: "short_text", Label: "Location", SystemKey: "location", Config: empty},
		{Section: profile, Type: "file_upload", Label: "CV", Required: true, SystemKey: "cv", Config: upload},
		{Section: profile, Type: "autofill_resume", Label: "Autofill with resume", SystemKey: "autofill_resume",
			Config: upload},
	}
}
