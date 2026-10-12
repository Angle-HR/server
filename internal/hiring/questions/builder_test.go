package questions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func counter() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("opt%d", n) }
}

func hasPath(errs []FieldError, path string) bool {
	for _, e := range errs {
		if e.Path == path {
			return true
		}
	}
	return false
}

func TestDefaultFormIsValid(t *testing.T) {
	out, _, errs := Build(DefaultForm(), nil, Policy{})
	if len(errs) != 0 {
		t.Fatalf("default form should be valid: %v", errs)
	}
	for i, q := range out {
		if q.Position != i {
			t.Errorf("question %d has position %d", i, q.Position)
		}
	}
	if !out[0].Locked || !out[1].Locked || out[2].Locked {
		t.Error("only full name and email should be locked")
	}
}

func TestBuildAssignsStableOptionIDs(t *testing.T) {
	form := DefaultForm()
	form = append(form, FormQuestion{
		Section: "screening", Type: "single_choice", Label: "Can you work on site?",
		Config: json.RawMessage(`{"options":[{"label":"Yes"},{"id":"keep","label":"No"},{"label":"Maybe"}]}`),
	})
	out, _, errs := Build(form, nil, Policy{NewID: counter()})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	var c choiceConfig
	if err := json.Unmarshal(out[len(out)-1].Config, &c); err != nil {
		t.Fatal(err)
	}
	got := []string{c.Options[0].ID, c.Options[1].ID, c.Options[2].ID}
	want := []string{"opt1", "keep", "opt2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("option ids = %v, want %v", got, want)
		}
	}
	// Saving again keeps the ids it already has.
	again, _, errs := Build(out, nil, Policy{NewID: counter()})
	if len(errs) != 0 || string(again[len(again)-1].Config) != string(out[len(out)-1].Config) {
		t.Errorf("ids changed on re-save: %v", errs)
	}
}

func TestBuildSystemFieldRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(f []FormQuestion) []FormQuestion
		path   string
	}{
		{"remove full name", func(f []FormQuestion) []FormQuestion { return f[1:] }, "questions"},
		{"remove email", func(f []FormQuestion) []FormQuestion { return append(f[:1:1], f[2:]...) }, "questions"},
		{"full name optional", func(f []FormQuestion) []FormQuestion { f[0].Required = false; return f }, "questions[0].required"},
		{"email wrong type", func(f []FormQuestion) []FormQuestion { f[1].Type = "short_text"; return f }, "questions[1].type"},
		{"name in wrong section", func(f []FormQuestion) []FormQuestion { f[0].Section = "profile"; return f }, "questions[0].section"},
		{"unknown system key", func(f []FormQuestion) []FormQuestion { f[2].SystemKey = "salary"; return f }, "questions[2].system_key"},
		{"cv accepts docx", func(f []FormQuestion) []FormQuestion {
			f[4].Config = json.RawMessage(`{"accept":["pdf","docx"]}`)
			return f
		}, "questions[4].config"},
		{"two cv fields", func(f []FormQuestion) []FormQuestion { return append(f, f[4]) }, "questions[6].system_key"},
		{"two autofills", func(f []FormQuestion) []FormQuestion {
			return append(f, FormQuestion{Section: "profile", Type: "autofill_resume", Label: "Again", Config: json.RawMessage(`{}`)})
		}, "questions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := Build(tc.mutate(DefaultForm()), nil, Policy{})
			if !hasPath(errs, tc.path) {
				t.Errorf("want an error at %s, got %v", tc.path, errs)
			}
		})
	}
}

func TestOptionalSystemFieldsCanBeRemoved(t *testing.T) {
	f := DefaultForm()
	f = append(f[:3:3], f[4:]...) // drop location
	f = f[:len(f)-1]              // drop autofill
	f[3].Required = false         // the CV becomes optional
	if _, _, errs := Build(f, nil, Policy{}); len(errs) != 0 {
		t.Errorf("location, autofill and CV requirement are optional: %v", errs)
	}
}

func TestPayHistoryIsBlockedButExpectationsAreNot(t *testing.T) {
	blocked := []string{
		"What is your current salary?", "Please share your previous salary", "Salary history",
		"How much do you currently earn?", "What was your last drawn salary?", "Current CTC", "What are you paid today?",
		"Share your last pay slip", "How much did you make at your last job?",
	}
	allowed := []string{
		"What are your salary expectations?", "Expected salary", "Desired pay", "What is your target compensation?",
		"What minimum salary would you accept?", "Expected CTC", "What is your current salary expectation?",
		"Are you comfortable with the salary range?", "Describe your experience with payroll systems",
	}
	for _, s := range blocked {
		if !PayHistoryText(s) {
			t.Errorf("%q should be blocked", s)
		}
	}
	for _, s := range allowed {
		if PayHistoryText(s) {
			t.Errorf("%q should be allowed", s)
		}
	}
	f := append(DefaultForm(), FormQuestion{Section: "screening", Type: "short_text", Label: "What is your current salary?", Config: json.RawMessage(`{}`)})
	if _, _, errs := Build(f, nil, Policy{}); !hasPath(errs, "questions[6].label") {
		t.Errorf("pay history question should be rejected: %v", errs)
	}
	opt := append(DefaultForm(), FormQuestion{Section: "screening", Type: "dropdown", Label: "Range",
		Config: json.RawMessage(`{"options":[{"label":"Salary history above 50k"},{"label":"Less"}]}`)})
	if _, _, errs := Build(opt, nil, Policy{}); !hasPath(errs, "questions[6].label") {
		t.Errorf("pay history in an option should be rejected: %v", errs)
	}
}

func choiceQ(ko string) FormQuestion {
	return FormQuestion{
		Section: "screening", Type: "single_choice", Label: "Do you have the right to work in the UK?", Required: true,
		Config:   json.RawMessage(`{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`),
		Knockout: json.RawMessage(ko),
	}
}

func TestKnockoutRules(t *testing.T) {
	on := Policy{AllowKnockout: true}
	tests := []struct {
		name    string
		q       FormQuestion
		policy  Policy
		wantErr bool
	}{
		{"valid choice", choiceQ(`{"option_ids":["no"],"reason":"Needs right to work"}`), on, false},
		{"disabled by company", choiceQ(`{"option_ids":["no"]}`), Policy{}, true},
		{"no options listed", choiceQ(`{"option_ids":[]}`), on, true},
		{"unknown option", choiceQ(`{"option_ids":["maybe"]}`), on, true},
		{"unknown field", choiceQ(`{"option_ids":["no"],"reject":true}`), on, true},
		{"match on single choice", choiceQ(`{"option_ids":["no"],"match":"all"}`), on, true},
		{"number ok", FormQuestion{Section: "screening", Type: "number", Label: "Years", Config: json.RawMessage(`{}`),
			Knockout: json.RawMessage(`{"operator":"lt","value":2}`)}, on, false},
		{"number bad operator", FormQuestion{Section: "screening", Type: "number", Label: "Years", Config: json.RawMessage(`{}`),
			Knockout: json.RawMessage(`{"operator":"around","value":2}`)}, on, true},
		{"number no value", FormQuestion{Section: "screening", Type: "number", Label: "Years", Config: json.RawMessage(`{}`),
			Knockout: json.RawMessage(`{"operator":"lt"}`)}, on, true},
		{"unsupported type", FormQuestion{Section: "screening", Type: "short_text", Label: "Why us?", Config: json.RawMessage(`{}`),
			Knockout: json.RawMessage(`{"operator":"lt","value":1}`)}, on, true},
		{"null knockout is none", choiceQ(`null`), Policy{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := Build(append(DefaultForm(), tc.q), nil, tc.policy)
			if got := hasPath(errs, "questions[6].knockout"); got != tc.wantErr {
				t.Errorf("knockout error = %v, want %v (%v)", got, tc.wantErr, errs)
			}
		})
	}
	m := FormQuestion{Section: "screening", Type: "checkbox", Label: "Which tools?", Config: json.RawMessage(
		`{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}`), Knockout: json.RawMessage(`{"option_ids":["a","b"],"match":"all"}`)}
	if _, _, errs := Build(append(DefaultForm(), m), nil, on); len(errs) != 0 {
		t.Errorf("checkbox knockout with match=all should pass: %v", errs)
	}
}

func TestSpecialCategories(t *testing.T) {
	q := FormQuestion{Section: "eligibility", Type: "single_choice", Label: "Do you have a disability?",
		Config:          json.RawMessage(`{"options":[{"id":"y","label":"Yes"},{"id":"n","label":"No"}]}`),
		SpecialCategory: "health_disability"}
	if _, _, errs := Build(append(DefaultForm(), q), nil, Policy{}); !hasPath(errs, "questions[6].special_category") {
		t.Errorf("undeclared category should be rejected: %v", errs)
	}
	decl := []Declaration{{Category: "health_disability", LegalCondition: "employment_law", Purpose: "Reasonable adjustments"}}
	if _, out, errs := Build(append(DefaultForm(), q), decl, Policy{}); len(errs) != 0 || len(out) != 1 {
		t.Errorf("declared category should pass: %v", errs)
	}
}

func TestDeclarationRules(t *testing.T) {
	tests := []struct {
		name string
		d    Declaration
		path string
	}{
		{"unknown category", Declaration{"politics", "explicit_consent", "x"}, "special_categories[0].category"},
		{"art 10 needed for criminal records", Declaration{"criminal_records", "explicit_consent", "Vetting"}, "special_categories[0].legal_condition"},
		{"art 9 needed for health", Declaration{"health_disability", "law_authorised", "Adjustments"}, "special_categories[0].legal_condition"},
		{"purpose required", Declaration{"religion_belief", "explicit_consent", " "}, "special_categories[0].purpose"},
		{"purpose too long", Declaration{"biometric", "explicit_consent", strings.Repeat("x", 301)}, "special_categories[0].purpose"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, errs := Build(DefaultForm(), []Declaration{tc.d}, Policy{})
			if !hasPath(errs, tc.path) {
				t.Errorf("want error at %s, got %v", tc.path, errs)
			}
		})
	}
	ok := []Declaration{
		{"criminal_records", "law_authorised", "Regulated role vetting"},
		{"race_ethnicity", "explicit_consent", "Equal opportunities monitoring"},
	}
	if _, out, errs := Build(DefaultForm(), ok, Policy{}); len(errs) != 0 || len(out) != 2 {
		t.Errorf("valid declarations rejected: %v", errs)
	}
	dup := append(ok, ok[1])
	if _, _, errs := Build(DefaultForm(), dup, Policy{}); !hasPath(errs, "special_categories[2].category") {
		t.Errorf("duplicate declaration should be rejected: %v", errs)
	}
}

func TestFormLimitsAndText(t *testing.T) {
	f := DefaultForm()
	for len(f) <= MaxQuestionsPerForm {
		f = append(f, FormQuestion{Section: "screening", Type: "short_text", Label: "Q", Config: json.RawMessage(`{}`)})
	}
	if _, _, errs := Build(f, nil, Policy{}); !hasPath(errs, "questions") {
		t.Errorf("too many questions should be rejected: %v", errs)
	}
	long := append(DefaultForm(), FormQuestion{Section: "screening", Type: "short_text", Label: strings.Repeat("é", 301), Config: json.RawMessage(`{}`)})
	if _, _, errs := Build(long, nil, Policy{}); !hasPath(errs, "questions[6].label") {
		t.Errorf("long label should be rejected: %v", errs)
	}
	empty := append(DefaultForm(), FormQuestion{Section: "screening", Type: "short_text", Label: "  ", Config: json.RawMessage(`{}`)})
	if _, _, errs := Build(empty, nil, Policy{}); !hasPath(errs, "questions[6].label") {
		t.Errorf("blank label should be rejected: %v", errs)
	}
	bad := append(DefaultForm(), FormQuestion{Section: "nope", Type: "unknown", Label: "Q"})
	if _, _, errs := Build(bad, nil, Policy{}); !hasPath(errs, "questions[6].section") || !hasPath(errs, "questions[6].config") {
		t.Errorf("unknown section and type should be reported: %v", errs)
	}
}

func TestBuildDoesNotMutateInput(t *testing.T) {
	in := DefaultForm()
	in[0].Label = "  Full name  "
	_, _, _ = Build(in, nil, Policy{})
	if in[0].Label != "  Full name  " || in[0].Locked {
		t.Error("Build must not modify its input")
	}
}
