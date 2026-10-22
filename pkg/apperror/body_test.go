package apperror

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeStrictTest(body string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func TestInvalidBody(t *testing.T) {
	type req struct {
		Email string `json:"email"`
		Age   int    `json:"age"`
	}
	cases := []struct{ name, body, path, msg string }{
		{"unknown field", `{"email":"a","nope":1}`, "nope", "unknown field"},
		{"wrong type", `{"email":5}`, "email", "must be a string"},
		{"wrong number type", `{"age":"x"}`, "age", "must be a number"},
		{"syntax", `{"email":`, "body", "the request body is not valid JSON"},
		{"broken", `{"email" "a"}`, "body", "the request body is not valid JSON"},
		{"empty", ``, "body", "the request body is empty"},
	}
	for _, c := range cases {
		var r req
		e := InvalidBody(decodeStrictTest(c.body, &r))
		if e.Code != CodeValidationError || e.Message != MsgInvalidRequestBody {
			t.Errorf("%s: code %q message %q", c.name, e.Code, e.Message)
		}
		fields, _ := e.Details["fields"].([]map[string]string)
		if len(fields) != 1 || fields[0]["path"] != c.path || fields[0]["message"] != c.msg {
			t.Errorf("%s: fields %v, want path %q message %q", c.name, e.Details["fields"], c.path, c.msg)
		}
	}
	if e := InvalidBody(nil); e.Details["fields"] == nil {
		t.Error("nil error must still name the body")
	}
}
