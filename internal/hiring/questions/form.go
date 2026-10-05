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
	add := func(i int, field, msg string) {
		errs = append(errs, FieldError{Path: fmt.Sprintf("questions[%d].%s", i, field), Message: msg})
	}

	if len(qs) > MaxQuestionsPerForm {
		errs = append(errs, FieldError{Path: "questions", Message: fmt.Sprintf("a form can have at most %d questions", MaxQuestionsPerForm)})
	}

	hasName, hasEmail, autofill := false, false, 0
	for i, q := range qs {
		if !Sections[q.Section] {
			add(i, "section", "unknown section")
		}
		if q.Label == "" {
			add(i, "label", "label is required")
		}
		if err := ValidateConfig(q.Type, q.Config); err != nil {
			add(i, "config", err.Error())
		}
		if q.SpecialCategory != "" {
			if !SpecialCategories[q.SpecialCategory] {
				add(i, "special_category", "unknown special category")
			} else if !declared[q.SpecialCategory] {
				add(i, "special_category", "this category has not been declared for the job")
			}
		}
		switch q.SystemKey {
		case "full_name":
			hasName = true
			if !q.Required {
				add(i, "required", "full name is always required")
			}
		case "email":
			hasEmail = true
			if !q.Required {
				add(i, "required", "email is always required")
			}
		}
		if q.Type == "autofill_resume" || q.SystemKey == "autofill_resume" {
			autofill++
		}
	}
	if !hasName {
		errs = append(errs, FieldError{Path: "questions", Message: "the full name field cannot be removed"})
	}
	if !hasEmail {
		errs = append(errs, FieldError{Path: "questions", Message: "the email field cannot be removed"})
	}
	if autofill > 1 {
		errs = append(errs, FieldError{Path: "questions", Message: "autofill with resume can appear only once"})
	}
	return errs
}
