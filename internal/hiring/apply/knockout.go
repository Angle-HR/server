package apply

import (
	"encoding/json"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/questions"
	"github.com/Angle-HR/server/internal/hiring/screening"
)

// MarketFor maps a candidate's country (ISO 3166-1 alpha-2) to the hiring market that covers it. The United
// Kingdom is its own market (GB); the EU market covers its member states; every other launch market is one
// country. The second result is false for a country no market covers.
func MarketFor(country string) (string, bool) {
	c := strings.ToUpper(strings.TrimSpace(country))
	switch {
	case c == "GB":
		return "UK", true
	case euMembers[c]:
		return "EU", true
	case c == "US", c == "IN", c == "NG", c == "KE", c == "ZA":
		return c, true
	}
	return "", false
}

var euMembers = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range strings.Fields("AT BE BG HR CY CZ DK EE FI FR DE GR HU IE IT LV LT LU MT NL PL PT RO SK SI ES SE") {
		m[c] = true
	}
	return m
}()

type choiceKnockout struct {
	OptionIDs []string `json:"option_ids"`
	Match     string   `json:"match"`
	Reason    string   `json:"reason"`
}

type numberKnockout struct {
	Operator string   `json:"operator"`
	Value    *float64 `json:"value"`
	Reason   string   `json:"reason"`
}

const defaultKnockoutReason = "Answer flagged by a screening question"

// knockoutFlags evaluates the per-question knockout rules of the frozen form. A knockout never rejects anyone:
// it flags the application so a person reviews it. An unanswered question never flags.
func knockoutFlags(form []questions.FormQuestion, answers map[string]json.RawMessage) []Flag {
	var out []Flag
	for i := range form {
		q := &form[i]
		if k := string(q.Knockout); k == "" || k == "null" {
			continue
		}
		raw, ok := answers[q.ID]
		if !ok {
			continue
		}
		switch q.Type {
		case "single_choice", "dropdown", "multiple_choice", "checkbox":
			var k choiceKnockout
			if json.Unmarshal(q.Knockout, &k) != nil || len(k.OptionIDs) == 0 {
				continue
			}
			picked := pickedOptions(raw)
			if choiceHit(k, picked) {
				out = append(out, Flag{QuestionID: q.ID, Reason: reason(k.Reason)})
			}
		case "number", "linear_scale":
			var k numberKnockout
			if json.Unmarshal(q.Knockout, &k) != nil || k.Value == nil {
				continue
			}
			// The screening package already compares numbers; reuse it through a one-rule evaluation.
			val, _ := json.Marshal(*k.Value)
			rule := screening.Rule{QuestionID: q.ID, Operator: k.Operator, Value: val, Reason: reason(k.Reason)}
			out = append(out, screening.Evaluate([]screening.Rule{rule}, map[string]json.RawMessage{q.ID: raw})...)
		}
	}
	return out
}

func choiceHit(k choiceKnockout, picked []string) bool {
	has := map[string]bool{}
	for _, p := range picked {
		has[p] = true
	}
	if k.Match == "all" {
		for _, id := range k.OptionIDs {
			if !has[id] {
				return false
			}
		}
		return true
	}
	for _, id := range k.OptionIDs {
		if has[id] {
			return true
		}
	}
	return false
}

func pickedOptions(raw json.RawMessage) []string {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	return nil
}

func reason(r string) string {
	if r = strings.TrimSpace(r); r != "" {
		return r
	}
	return defaultKnockoutReason
}
