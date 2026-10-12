// Package screening holds the job-level screening rules. A rule never rejects anyone: when an application
// matches, the candidate is flagged and a person reviews them (7 Oct decision, pending counsel; there is no
// automatic rejection in any market at launch). Rules count as automated screening, so they need Legal to have
// switched screening on and the company DPIA to exist before the job publishes (see package gates).
//
// A rule points at one question of the job's saved form. Questions can also carry their own knockout (see
// package questions); rules exist for the cases that sit above a single question and for the dedicated
// Screening step of the designs.
package screening

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/hiring/questions"
)

// Operators. Choice questions use In and NotIn with option ids; number and scale questions use the
// comparison operators.
const (
	OpIn    = "in"     // the candidate picked at least one of these options
	OpNotIn = "not_in" // the candidate picked none of these options
	OpLT    = "lt"
	OpLTE   = "lte"
	OpGT    = "gt"
	OpGTE   = "gte"
	OpEQ    = "eq"
	OpNEQ   = "neq"
)

// Limits.
const (
	MaxRules     = 20
	MaxReasonLen = 300
)

// Rule is one screening rule as the API sends and returns it.
type Rule struct {
	ID         string          `json:"id,omitempty"`
	QuestionID string          `json:"question_id"`
	Operator   string          `json:"operator"`
	Value      json.RawMessage `json:"value"`
	Reason     string          `json:"reason"`
}

// FieldError points at the part of the request that is wrong, e.g. rules[1].value.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func choiceType(t string) bool {
	switch t {
	case "single_choice", "dropdown", "multiple_choice", "checkbox":
		return true
	}
	return false
}

func numberType(t string) bool { return t == "number" || t == "linear_scale" }

var numberOps = map[string]bool{OpLT: true, OpLTE: true, OpGT: true, OpGTE: true, OpEQ: true, OpNEQ: true}

// Validate checks a whole rule set against the job's saved form and returns one error per problem. The reason
// is required because the reviewer, and any candidate who asks for human review, must be able to see why the
// candidate was flagged.
func Validate(rules []Rule, form []questions.FormQuestion) []FieldError {
	var errs []FieldError
	add := func(i int, field, msg string) {
		errs = append(errs, FieldError{Path: fmt.Sprintf("rules[%d].%s", i, field), Message: msg})
	}
	if len(rules) > MaxRules {
		return []FieldError{{Path: "rules", Message: fmt.Sprintf("at most %d rules", MaxRules)}}
	}
	byID := make(map[string]*questions.FormQuestion, len(form))
	for i := range form {
		if form[i].ID != "" {
			byID[form[i].ID] = &form[i]
		}
	}
	seen := map[string]bool{}
	for i, r := range rules {
		q := byID[r.QuestionID]
		if q == nil {
			add(i, "question_id", "this question is not on the application form")
			continue
		}
		key := r.QuestionID + "|" + r.Operator + "|" + string(r.Value)
		if seen[key] {
			add(i, "operator", "this rule is already listed")
		}
		seen[key] = true

		reason := strings.TrimSpace(r.Reason)
		if reason == "" {
			add(i, "reason", "say why this flags a candidate")
		} else if utf8.RuneCountInString(reason) > MaxReasonLen {
			add(i, "reason", fmt.Sprintf("reason must be at most %d characters", MaxReasonLen))
		}

		switch {
		case choiceType(q.Type):
			if msg := checkChoice(r, q); msg != "" {
				add(i, "value", msg)
			}
		case numberType(q.Type):
			if msg := checkNumber(r); msg != "" {
				add(i, "value", msg)
			}
		default:
			add(i, "question_id", "rules are not available for this question type")
		}
	}
	return errs
}

type option struct {
	ID string `json:"id"`
}

func optionIDs(cfg json.RawMessage) map[string]bool {
	var c struct {
		Options []option `json:"options"`
	}
	out := map[string]bool{}
	if json.Unmarshal(cfg, &c) != nil {
		return out
	}
	for _, o := range c.Options {
		out[o.ID] = true
	}
	return out
}

func checkChoice(r Rule, q *questions.FormQuestion) string {
	if r.Operator != OpIn && r.Operator != OpNotIn {
		return "a choice question needs the operator in or not_in"
	}
	var ids []string
	if json.Unmarshal(r.Value, &ids) != nil || len(ids) == 0 {
		return "value must be a list of option ids"
	}
	valid := optionIDs(q.Config)
	for _, id := range ids {
		if !valid[id] {
			return "value refers to an option that does not exist"
		}
	}
	return ""
}

func checkNumber(r Rule) string {
	if !numberOps[r.Operator] {
		return "a number question needs one of lt, lte, gt, gte, eq, neq"
	}
	var v float64
	if json.Unmarshal(r.Value, &v) != nil {
		return "value must be a number"
	}
	return ""
}

// Flag says why an application was flagged for review.
type Flag struct {
	RuleID     string `json:"rule_id,omitempty"`
	QuestionID string `json:"question_id"`
	Reason     string `json:"reason"`
}

// Evaluate runs the rules over a candidate's answers (question id -> the stored JSON answer) and returns the
// flags. An unanswered question never flags anyone: absence of an answer is not a reason to hold a person back
// (and required questions are enforced when the application is submitted). Nothing here rejects.
func Evaluate(rules []Rule, answers map[string]json.RawMessage) []Flag {
	var out []Flag
	for _, r := range rules {
		raw, ok := answers[r.QuestionID]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if matches(r, raw) {
			out = append(out, Flag{RuleID: r.ID, QuestionID: r.QuestionID, Reason: strings.TrimSpace(r.Reason)})
		}
	}
	return out
}

func matches(r Rule, answer json.RawMessage) bool {
	switch r.Operator {
	case OpIn, OpNotIn:
		var want []string
		if json.Unmarshal(r.Value, &want) != nil {
			return false
		}
		got, ok := picked(answer)
		if !ok {
			return false
		}
		hit := false
		for _, g := range got {
			for _, w := range want {
				if g == w {
					hit = true
				}
			}
		}
		if r.Operator == OpIn {
			return hit
		}
		return !hit
	case OpLT, OpLTE, OpGT, OpGTE, OpEQ, OpNEQ:
		var want, got float64
		if json.Unmarshal(r.Value, &want) != nil || json.Unmarshal(answer, &got) != nil {
			return false
		}
		switch r.Operator {
		case OpLT:
			return got < want
		case OpLTE:
			return got <= want
		case OpGT:
			return got > want
		case OpGTE:
			return got >= want
		case OpEQ:
			return got == want
		default:
			return got != want
		}
	}
	return false
}

// picked reads a choice answer: one option id, or a list of them.
func picked(answer json.RawMessage) ([]string, bool) {
	var one string
	if json.Unmarshal(answer, &one) == nil {
		return []string{one}, true
	}
	var many []string
	if json.Unmarshal(answer, &many) == nil {
		return many, true
	}
	return nil, false
}
