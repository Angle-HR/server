package questions

import (
	"encoding/json"
	"fmt"
)

// Sections a question can belong to.
var Sections = map[string]bool{
	"personal_information": true,
	"profile":              true,
	"eligibility":          true,
	"screening":            true,
}

// SpecialCategories that a question can be tagged with.
var SpecialCategories = map[string]bool{
	"health_disability": true,
	"race_ethnicity":    true,
	"religion_belief":   true,
	"biometric":         true,
	"criminal_records":  true,
}

// Question is one question as saved by the form builder.
type Question struct {
	Section         string          `json:"section"`
	Type            string          `json:"type"`
	Label           string          `json:"label"`
	Required        bool            `json:"required"`
	Config          json.RawMessage `json:"config"`
	SystemKey       string          `json:"system_key,omitempty"`
	SpecialCategory string          `json:"special_category,omitempty"`
}

// FieldError points at the part of the form that is wrong, e.g. questions[3].config.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidateForm checks a whole form and returns one error per problem.
// declared is the set of special categories the employer has declared a basis for.
func ValidateForm(qs []Question, declared map[string]bool) []FieldError {
	var errs []FieldError
	if len(qs) > MaxQuestionsPerForm {
		errs = append(errs, FieldError{
			Path:    "questions",
			Message: fmt.Sprintf("a form can have at most %d questions", MaxQuestionsPerForm),
		})
	}

	var seen formCoverage
	for i := range qs {
		errs = append(errs, validateQuestion(i, &qs[i], declared)...)
		seen.note(&qs[i])
	}
	return append(errs, seen.problems()...)
}

// validateQuestion checks a single question.
func validateQuestion(i int, q *Question, declared map[string]bool) []FieldError {
	var errs []FieldError
	add := func(field, msg string) {
		errs = append(errs, FieldError{Path: fmt.Sprintf("questions[%d].%s", i, field), Message: msg})
	}

	if !Sections[q.Section] {
		add("section", "unknown section")
	}
	if q.Label == "" {
		add("label", "label is required")
	}
	if err := ValidateConfig(q.Type, q.Config); err != nil {
		add("config", err.Error())
	}
	switch {
	case q.SpecialCategory == "":
	case !SpecialCategories[q.SpecialCategory]:
		add("special_category", "unknown special category")
	case !declared[q.SpecialCategory]:
		add("special_category", "this category has not been declared for the job")
	}
	switch q.SystemKey {
	case "full_name":
		if !q.Required {
			add("required", "full name is always required")
		}
	case "email":
		if !q.Required {
			add("required", "email is always required")
		}
	}
	return errs
}

// formCoverage tracks which mandatory fields a form contains.
type formCoverage struct {
	hasName, hasEmail bool
	autofill          int
}

func (c *formCoverage) note(q *Question) {
	switch q.SystemKey {
	case "full_name":
		c.hasName = true
	case "email":
		c.hasEmail = true
	}
	if q.Type == "autofill_resume" || q.SystemKey == "autofill_resume" {
		c.autofill++
	}
}

// problems returns the form-level errors for missing or duplicated fields.
func (c *formCoverage) problems() []FieldError {
	var errs []FieldError
	if !c.hasName {
		errs = append(errs, FieldError{Path: "questions", Message: "the full name field cannot be removed"})
	}
	if !c.hasEmail {
		errs = append(errs, FieldError{Path: "questions", Message: "the email field cannot be removed"})
	}
	if c.autofill > 1 {
		errs = append(errs, FieldError{Path: "questions", Message: "autofill with resume can appear only once"})
	}
	return errs
}
