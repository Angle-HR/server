package hiringhttp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/rbac"
)

type stubCompany struct{ state draft.CompanyState }

func (s *stubCompany) State(context.Context, string) (draft.CompanyState, error) { return s.state, nil }

const publishableBody = `{
 "title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time",
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true},
 "lawful_basis":"contract"}`

func publishEnvHTTP(t *testing.T, role rbac.Role) (*env, *stubCompany) {
	t.Helper()
	e := newEnv(role)
	co := &stubCompany{state: draft.CompanyState{Verified: true, DPAAccepted: true, PrivacyContactSet: true}}
	e.dir.svc.Company = co
	n := 0
	e.dir.svc.NewPublicID = func() string { n++; return "jpub" + string(rune('a'+n-1)) }
	e.dir.svc.Ref.(*drafttest.Ref).GateList = []gates.Gate{{
		ID: "uk-lawful", MarketCode: "UK", Severity: gates.SeverityRequired, Requirement: "Lawful basis recorded.",
		Version: 1, Trigger: json.RawMessage(`{"type":"always"}`),
	}}
	return e, co
}

func TestPublishBlockedThenPublishedOverHTTP(t *testing.T) {
	e, co := publishEnvHTTP(t, rbac.RoleFounder)
	co.state = draft.CompanyState{}
	id := e.create(t, publishableBody)

	r := e.do(t, "POST", "/jobs/"+id+"/publish", "")
	if r.Status != 422 || r.Err.Code != "PUBLISH_BLOCKED" || r.Err.Details["blocking"] == nil {
		t.Fatalf("blocked: %d %+v", r.Status, r.Err)
	}
	if r = e.do(t, "GET", "/jobs/"+id+"/publish-check", ""); r.Status != 200 {
		t.Fatalf("check: %d %+v", r.Status, r.Err)
	}

	co.state = draft.CompanyState{Verified: true, DPAAccepted: true, PrivacyContactSet: true}
	if r = e.do(t, "PUT", "/jobs/"+id+"/compliance/uk-lawful", `{"confirmed":true}`); r.Status != 200 {
		t.Fatalf("confirm: %d %+v", r.Status, r.Err)
	}
	r = e.do(t, "POST", "/jobs/"+id+"/publish", "")
	if r.Status != 200 {
		t.Fatalf("publish: %d %+v %s", r.Status, r.Err, r.Data)
	}
	for _, step := range []struct{ path, want string }{
		{"pause", "paused"}, {"resume", "published"}, {"close", "closed"}, {"archive", "archived"},
	} {
		if r = e.do(t, "POST", "/jobs/"+id+"/"+step.path, ""); r.Status != 200 {
			t.Fatalf("%s: %d %+v", step.path, r.Status, r.Err)
		}
	}
	// A draft cannot be paused.
	id2 := e.create(t, `{"title":"Cook"}`)
	if r = e.do(t, "POST", "/jobs/"+id2+"/pause", ""); r.Status != 409 {
		t.Fatalf("invalid transition: %d %+v", r.Status, r.Err)
	}
}

func TestBulkAndExportOverHTTP(t *testing.T) {
	e, _ := publishEnvHTTP(t, rbac.RoleFounder)
	id := e.create(t, `{"title":"Cook"}`)
	r := e.do(t, "POST", "/jobs/bulk", `{"action":"archive","ids":["`+id+`"]}`)
	if r.Status != 200 {
		t.Fatalf("bulk: %d %+v", r.Status, r.Err)
	}
	req := httptest.NewRequest("GET", "/api/v1/jobs/export", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/csv") || !strings.Contains(rec.Body.String(), "Cook") {
		t.Fatalf("export: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestExportForbiddenForHR2(t *testing.T) {
	e, _ := publishEnvHTTP(t, rbac.RoleHR2)
	if r := e.do(t, "GET", "/jobs/export", ""); r.Status != 403 {
		t.Fatalf("export: %d", r.Status)
	}
}

func TestMembersAndRulesRoutes(t *testing.T) {
	e, _ := publishEnvHTTP(t, rbac.RoleFounder)
	id := e.create(t, `{"title":"Cook"}`)
	if r := e.do(t, "GET", "/jobs/"+id+"/members", ""); r.Status != 200 {
		t.Fatalf("members: %d %+v", r.Status, r.Err)
	}
	if r := e.do(t, "GET", "/jobs/"+id+"/disqualification-rules", ""); r.Status != 200 {
		t.Fatalf("rules: %d %+v", r.Status, r.Err)
	}
	r := e.do(t, "PUT", "/jobs/"+id+"/disqualification-rules", `{"rules":[{"question_id":"x","operator":"eq","value":"no"}]}`)
	if r.Status == 200 {
		t.Fatalf("rules should be refused without screening enabled")
	}
}

func TestCompanyRoutes(t *testing.T) {
	e, _ := publishEnvHTTP(t, rbac.RoleFounder)
	if r := e.do(t, "GET", "/organization/agreements", ""); r.Status != 200 {
		t.Fatalf("agreements: %d %+v", r.Status, r.Err)
	}
	r := e.do(t, "PUT", "/organization/privacy-contact", `{"email":"not-an-email"}`)
	if r.Status != 400 {
		t.Fatalf("bad email: %d %+v", r.Status, r.Err)
	}
	_ = strings.TrimSpace
}
