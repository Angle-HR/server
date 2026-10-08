package screening

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/questions"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

var form = []questions.FormQuestion{
	{ID: "q-visa", Type: "single_choice", Config: raw(`{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}`)},
	{ID: "q-years", Type: "number", Config: raw(`{}`)},
	{ID: "q-text", Type: "short_text", Config: raw(`{}`)},
	{ID: "q-multi", Type: "multiple_choice", Config: raw(`{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}`)},
}

func TestValidate(t *testing.T) {
	good := []Rule{
		{QuestionID: "q-visa", Operator: OpIn, Value: raw(`["no"]`), Reason: "Needs sponsorship"},
		{QuestionID: "q-years", Operator: OpLT, Value: raw(`3`), Reason: "Under three years"},
	}
	if errs := Validate(good, form); len(errs) != 0 {
		t.Fatalf("good rules: %v", errs)
	}
	bad := []struct {
		name string
		r    Rule
		path string
	}{
		{"unknown question", Rule{QuestionID: "nope", Operator: OpIn, Value: raw(`["x"]`), Reason: "r"}, "rules[0].question_id"},
		{"no reason", Rule{QuestionID: "q-years", Operator: OpLT, Value: raw(`3`)}, "rules[0].reason"},
		{"long reason", Rule{QuestionID: "q-years", Operator: OpLT, Value: raw(`3`), Reason: strings.Repeat("x", 301)}, "rules[0].reason"},
		{"unknown option", Rule{QuestionID: "q-visa", Operator: OpIn, Value: raw(`["maybe"]`), Reason: "r"}, "rules[0].value"},
		{"wrong operator for choice", Rule{QuestionID: "q-visa", Operator: OpLT, Value: raw(`["no"]`), Reason: "r"}, "rules[0].value"},
		{"wrong operator for number", Rule{QuestionID: "q-years", Operator: OpIn, Value: raw(`3`), Reason: "r"}, "rules[0].value"},
		{"number value not a number", Rule{QuestionID: "q-years", Operator: OpGT, Value: raw(`"x"`), Reason: "r"}, "rules[0].value"},
		{"text question", Rule{QuestionID: "q-text", Operator: OpEQ, Value: raw(`1`), Reason: "r"}, "rules[0].question_id"},
		{"empty list", Rule{QuestionID: "q-visa", Operator: OpIn, Value: raw(`[]`), Reason: "r"}, "rules[0].value"},
	}
	for _, c := range bad {
		errs := Validate([]Rule{c.r}, form)
		if len(errs) == 0 || errs[0].Path != c.path {
			t.Errorf("%s: %v", c.name, errs)
		}
	}
	dup := []Rule{good[0], good[0]}
	if errs := Validate(dup, form); len(errs) != 1 || errs[0].Path != "rules[1].operator" {
		t.Errorf("duplicate: %v", errs)
	}
	many := make([]Rule, MaxRules+1)
	if errs := Validate(many, form); len(errs) != 1 || errs[0].Path != "rules" {
		t.Errorf("too many: %v", errs)
	}
}

func TestEvaluate(t *testing.T) {
	rules := []Rule{
		{ID: "r1", QuestionID: "q-visa", Operator: OpIn, Value: raw(`["no"]`), Reason: "Needs sponsorship"},
		{ID: "r2", QuestionID: "q-years", Operator: OpLT, Value: raw(`3`), Reason: " Under three years "},
		{ID: "r3", QuestionID: "q-multi", Operator: OpNotIn, Value: raw(`["a"]`), Reason: "Missing A"},
	}
	flags := Evaluate(rules, map[string]json.RawMessage{
		"q-visa": raw(`"no"`), "q-years": raw(`2`), "q-multi": raw(`["b"]`),
	})
	if len(flags) != 3 || flags[1].Reason != "Under three years" {
		t.Errorf("flags = %+v", flags)
	}
	flags = Evaluate(rules, map[string]json.RawMessage{
		"q-visa": raw(`"yes"`), "q-years": raw(`3`), "q-multi": raw(`["a","b"]`),
	})
	if len(flags) != 0 {
		t.Errorf("clean application flagged: %+v", flags)
	}
}

func TestEvaluateIgnoresMissingAnswers(t *testing.T) {
	rules := []Rule{{QuestionID: "q-multi", Operator: OpNotIn, Value: raw(`["a"]`), Reason: "r"}}
	for _, answers := range []map[string]json.RawMessage{
		{}, {"q-multi": nil}, {"q-multi": raw(`null`)},
	} {
		if f := Evaluate(rules, answers); len(f) != 0 {
			t.Errorf("an unanswered question must not flag: %+v", f)
		}
	}
	// A malformed answer never flags either.
	if f := Evaluate(rules, map[string]json.RawMessage{"q-multi": raw(`{"x":1}`)}); len(f) != 0 {
		t.Errorf("malformed answer flagged: %+v", f)
	}
}
