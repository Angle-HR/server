package questions

import (
	"encoding/json"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestAllTypesRegistered(t *testing.T) {
	want := []string{"short_text", "long_text", "number", "email", "phone_number", "single_choice",
		"multiple_choice", "checkbox", "dropdown", "linear_scale", "image_upload", "file_upload",
		"link", "date", "time", "autofill_resume"}
	for _, k := range want {
		if _, err := Get(k); err != nil {
			t.Errorf("type %s not registered", k)
		}
	}
	if len(Keys()) != len(want) {
		t.Errorf("registered %d types, want %d", len(Keys()), len(want))
	}
	if _, err := Get("nope"); err == nil {
		t.Error("unknown type should error")
	}
}

func TestValidateConfig(t *testing.T) {
	opts := `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}`
	tests := []struct {
		name, typ, cfg string
		wantErr        bool
	}{
		{"empty text ok", "short_text", ``, false},
		{"unknown field", "short_text", `{"x":1}`, true},
		{"negative max", "long_text", `{"max_length":-1}`, true},
		{"number min>max", "number", `{"min":5,"max":1}`, true},
		{"choice ok", "single_choice", opts, false},
		{"choice one option", "single_choice", `{"options":[{"id":"a","label":"A"}]}`, true},
		{"choice dup id", "dropdown", `{"options":[{"id":"a","label":"A"},{"id":"a","label":"B"}]}`, true},
		{"single with min_selected", "single_choice", `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"min_selected":1}`, true},
		{"multi limits ok", "multiple_choice", `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"min_selected":1,"max_selected":2}`, false},
		{"multi min>max", "multiple_choice", `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"min_selected":2,"max_selected":1}`, true},
		{"searchable only dropdown", "single_choice", `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"searchable":true}`, true},
		{"scale ok", "linear_scale", `{"min":1,"max":5}`, false},
		{"scale bad min", "linear_scale", `{"min":2,"max":5}`, true},
		{"scale max too big", "linear_scale", `{"min":0,"max":11}`, true},
		{"image svg rejected", "image_upload", `{"accept":["svg"]}`, true},
		{"image png ok", "image_upload", `{"accept":["png"],"max_mb":5}`, false},
		{"file docx ok", "file_upload", `{"accept":["docx"]}`, false},
		{"resume docx rejected", "autofill_resume", `{"accept":["docx"]}`, true},
		{"date bad", "date", `{"min_date":"nope"}`, true},
		{"date order", "date", `{"min_date":"2026-02-01","max_date":"2026-01-01"}`, true},
		{"time ok", "time", `{}`, false},
		{"email extra", "email", `{"a":1}`, true},
		{"phone bad country", "phone_number", `{"default_country":"GBR"}`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateConfig(tc.typ, raw(tc.cfg))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateAnswer(t *testing.T) {
	opts := `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"},{"id":"c","label":"C"}]}`
	multi := `{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"},{"id":"c","label":"C"}],"min_selected":1,"max_selected":2}`
	tests := []struct {
		name, typ, cfg, ans string
		wantErr             bool
	}{
		{"short ok", "short_text", `{"max_length":5}`, `"hello"`, false},
		{"short too long", "short_text", `{"max_length":5}`, `"hello!"`, true},
		{"short not string", "short_text", `{}`, `3`, true},
		{"long default limit", "long_text", `{}`, `"` + string(make([]byte, 0)) + `ok"`, false},
		{"number in range", "number", `{"min":1,"max":10}`, `5`, false},
		{"number low", "number", `{"min":1}`, `0`, true},
		{"number high", "number", `{"max":10}`, `11`, true},
		{"email ok", "email", `{}`, `"a@b.com"`, false},
		{"email bad", "email", `{}`, `"a@b"`, true},
		{"email display name rejected", "email", `{}`, `"Bob <a@b.com>"`, true},
		{"phone ok", "phone_number", `{}`, `"+2348012345678"`, false},
		{"phone no plus", "phone_number", `{}`, `"08012345678"`, true},
		{"phone letters", "phone_number", `{}`, `"+23480abc"`, true},
		{"single ok", "single_choice", opts, `"a"`, false},
		{"single unknown", "single_choice", opts, `"z"`, true},
		{"single list", "single_choice", opts, `["a"]`, true},
		{"multi ok", "multiple_choice", multi, `["a","b"]`, false},
		{"multi too many", "multiple_choice", multi, `["a","b","c"]`, true},
		{"multi too few", "multiple_choice", multi, `[]`, true},
		{"multi dup", "multiple_choice", multi, `["a","a"]`, true},
		{"multi unknown", "checkbox", opts, `["z"]`, true},
		{"dropdown ok", "dropdown", opts, `"b"`, false},
		{"scale ok", "linear_scale", `{"min":1,"max":5}`, `3`, false},
		{"scale out", "linear_scale", `{"min":1,"max":5}`, `6`, true},
		{"scale fraction", "linear_scale", `{"min":1,"max":5}`, `2.5`, true},
		{"upload ok", "file_upload", `{}`, `"uploads/abc"`, false},
		{"upload empty", "file_upload", `{}`, `""`, true},
		{"link ok", "link", `{}`, `"https://github.com/x"`, false},
		{"link http", "link", `{}`, `"http://github.com/x"`, true},
		{"link javascript", "link", `{}`, `"javascript:alert(1)"`, true},
		{"link host allowed", "link", `{"allowed_hosts":["linkedin.com"]}`, `"https://www.linkedin.com/in/x"`, false},
		{"link host denied", "link", `{"allowed_hosts":["linkedin.com"]}`, `"https://evil.com/linkedin.com"`, true},
		{"date ok", "date", `{"min_date":"2026-01-01"}`, `"2026-05-01"`, false},
		{"date early", "date", `{"min_date":"2026-01-01"}`, `"2025-12-31"`, true},
		{"date format", "date", `{}`, `"01/02/2026"`, true},
		{"time ok", "time", `{}`, `"14:30"`, false},
		{"time bad", "time", `{}`, `"25:00"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateAnswer(tc.typ, raw(tc.cfg), raw(tc.ans))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateForm(t *testing.T) {
	base := func() []Question {
		return []Question{
			{Section: "personal_information", Type: "short_text", Label: "Full name", Required: true, SystemKey: "full_name"},
			{Section: "personal_information", Type: "email", Label: "Email", Required: true, SystemKey: "email"},
		}
	}
	if errs := ValidateForm(base(), nil); len(errs) != 0 {
		t.Fatalf("base form should be valid: %v", errs)
	}

	missing := base()[:1]
	if errs := ValidateForm(missing, nil); len(errs) == 0 {
		t.Error("removing email must fail")
	}

	optionalName := base()
	optionalName[0].Required = false
	if errs := ValidateForm(optionalName, nil); len(errs) == 0 {
		t.Error("optional full name must fail")
	}

	tagged := append(base(), Question{Section: "screening", Type: "single_choice", Label: "Religion",
		Config: raw(`{"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}`), SpecialCategory: "religion_belief"})
	if errs := ValidateForm(tagged, nil); len(errs) == 0 {
		t.Error("undeclared special category must fail")
	}
	if errs := ValidateForm(tagged, map[string]bool{"religion_belief": true}); len(errs) != 0 {
		t.Errorf("declared special category should pass: %v", errs)
	}

	two := append(base(),
		Question{Section: "profile", Type: "autofill_resume", Label: "Autofill"},
		Question{Section: "profile", Type: "autofill_resume", Label: "Autofill again"})
	if errs := ValidateForm(two, nil); len(errs) == 0 {
		t.Error("two autofill questions must fail")
	}

	bad := append(base(), Question{Section: "screening", Type: "single_choice", Label: "Pick", Config: raw(`{"options":[]}`)})
	errs := ValidateForm(bad, nil)
	if len(errs) != 1 || errs[0].Path != "questions[2].config" {
		t.Errorf("want path questions[2].config, got %v", errs)
	}

	many := make([]Question, 0, MaxQuestionsPerForm+1)
	many = append(many, base()...)
	for len(many) <= MaxQuestionsPerForm {
		many = append(many, Question{Section: "screening", Type: "short_text", Label: "Q"})
	}
	if errs := ValidateForm(many, nil); len(errs) == 0 {
		t.Error("over 50 questions must fail")
	}
}
