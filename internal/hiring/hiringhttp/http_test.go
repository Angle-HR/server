package hiringhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/rbac"
	"github.com/Angle-HR/server/internal/region"
)

const (
	org     = "bbbbbbbb-0000-4000-8000-000000000001"
	userA   = "aaaaaaaa-0000-4000-8000-000000000001"
	indTech = "33333333-3333-4333-8333-333333333333"
	dept    = "44444444-4444-4444-8444-444444444444"
)

type fakeDir struct {
	svc    *draft.Service
	caller draft.Caller
	err    error
}

func (d *fakeDir) Scope(context.Context, string, region.Region) (*draft.Service, draft.Caller, error) {
	return d.svc, d.caller, d.err
}

type env struct {
	h      http.Handler
	dir    *fakeDir
	store  *drafttest.Store
	signed bool
}

func newEnv(role rbac.Role) *env {
	st := drafttest.NewStore()
	ref := &drafttest.Ref{Skills: map[string]string{}, Industry: indTech}
	fixed := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	svc := &draft.Service{Store: st, Ref: ref, Now: func() time.Time { return fixed }}
	e := &env{store: st, signed: true}
	e.dir = &fakeDir{svc: svc, caller: draft.Caller{
		UserID: userA, OrgID: org, CompanyName: "Acme Ltd", Perms: rbac.NewSet(rbac.DefaultMatrix[role]...),
	}}
	hh := NewHiringHandler(e.dir, func(context.Context) (string, region.Region, bool) {
		return userA, region.Region("uk"), e.signed
	})
	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) { hh.RegisterProtectedRoutes(r) })
	e.h = r
	return e
}

type reply struct {
	Status int
	Header http.Header
	Data   json.RawMessage
	Err    struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	Meta map[string]any
}

func (e *env) do(t *testing.T, method, path, body string, hdr ...string) reply {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := reply{Status: rec.Code, Header: rec.Header()}
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
		Meta map[string]any `json:"meta"`
	}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s %s: bad json %q: %v", method, path, rec.Body.String(), err)
		}
	}
	out.Data, out.Meta = env.Data, env.Meta
	if env.Error != nil {
		out.Err.Code, out.Err.Message, out.Err.Details = env.Error.Code, env.Error.Message, env.Error.Details
	}
	return out
}

func (r reply) field(t *testing.T, name string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Data, &m); err != nil {
		t.Fatalf("data is not an object: %s", r.Data)
	}
	return m[name]
}

const fullBody = `{
 "title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time",
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true}}`

func (e *env) create(t *testing.T, body string) string {
	t.Helper()
	r := e.do(t, "POST", "/jobs", body)
	if r.Status != 201 {
		t.Fatalf("create: %d %+v", r.Status, r.Err)
	}
	return r.field(t, "id").(string)
}

func TestCreateGetPatchFlow(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	r := e.do(t, "POST", "/jobs", `{"title":"Cook"}`)
	if r.Status != 201 || r.Header.Get("ETag") != `"1"` || !strings.HasPrefix(r.Header.Get("Location"), "/api/v1/jobs/") {
		t.Fatalf("create: %d etag=%q loc=%q", r.Status, r.Header.Get("ETag"), r.Header.Get("Location"))
	}
	id := r.field(t, "id").(string)
	if r.field(t, "status") != "draft" || r.field(t, "job_code") != "JB-1" {
		t.Fatalf("unexpected job: %s", r.Data)
	}

	g := e.do(t, "GET", "/jobs/"+id, "")
	if g.Status != 200 || g.Header.Get("ETag") != `"1"` {
		t.Fatalf("get: %d %q", g.Status, g.Header.Get("ETag"))
	}

	p := e.do(t, "PATCH", "/jobs/"+id, `{"title":"Chef"}`, "If-Match", `"1"`)
	if p.Status != 200 || p.field(t, "title") != "Chef" || p.Header.Get("ETag") != `"2"` {
		t.Fatalf("patch: %d %s %+v", p.Status, p.Data, p.Err)
	}

	stale := e.do(t, "PATCH", "/jobs/"+id, `{"title":"Baker"}`, "If-Match", `"1"`)
	if stale.Status != 409 || stale.Err.Details["current_revision"] != float64(2) {
		t.Fatalf("stale: %d %+v", stale.Status, stale.Err)
	}
}

func TestBadInputIsRejected(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, `{}`)
	for name, tc := range map[string]struct{ method, path, body string }{
		"unknown field":   {"PATCH", "/jobs/" + id, `{"colour":"red"}`},
		"broken json":     {"PATCH", "/jobs/" + id, `{`},
		"create unknown":  {"POST", "/jobs", `{"nope":1}`},
		"markets unknown": {"PUT", "/jobs/" + id + "/markets", `{"places":[]}`},
		"form unknown":    {"PUT", "/jobs/" + id + "/application-form", `{"fields":[]}`},
		"bad limit":       {"GET", "/jobs?limit=abc", ""},
	} {
		if r := e.do(t, tc.method, tc.path, tc.body); r.Status != 400 || r.Err.Code != "VALIDATION_ERROR" {
			t.Errorf("%s: %d %+v", name, r.Status, r.Err)
		}
	}
	if r := e.do(t, "PATCH", "/jobs/"+id, `{}`, "If-Match", "banana"); r.Status != 400 {
		t.Errorf("bad If-Match: %d", r.Status)
	}
	if r := e.do(t, "GET", "/jobs/not-a-uuid", ""); r.Status != 404 {
		t.Errorf("bad id: %d", r.Status)
	}
}

func TestSaveAndContinueListsEveryMissingField(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, `{"title":"Cook"}`)
	r := e.do(t, "PUT", "/jobs/"+id+"/details", `{}`)
	if r.Status != 400 {
		t.Fatalf("status %d", r.Status)
	}
	fields, _ := r.Err.Details["fields"].([]any)
	if len(fields) < 5 {
		t.Fatalf("want every missing field listed, got %v", r.Err.Details)
	}
	ok := e.do(t, "PUT", "/jobs/"+id+"/details", fullBody)
	if ok.Status != 200 || ok.field(t, "current_step") != "application_form" {
		t.Fatalf("continue: %d %s %+v", ok.Status, ok.Data, ok.Err)
	}
}

func TestMarketsEndpoint(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, `{"location_mode":"specific_area"}`)
	if r := e.do(t, "PUT", "/jobs/"+id+"/markets", `{"markets":[{"market_code":"NG"}]}`); r.Status != 400 {
		t.Fatalf("closed market: %d", r.Status)
	}
	r := e.do(t, "PUT", "/jobs/"+id+"/markets", `{"location_mode":"specific_area","markets":[{"market_code":"KE"}]}`)
	if r.Status != 200 || r.field(t, "retention_months") != float64(12) {
		t.Fatalf("kenya: %d %s %+v", r.Status, r.Data, r.Err)
	}
}

func TestPermissionsAndAuth(t *testing.T) {
	e := newEnv(rbac.RoleEmployee)
	if r := e.do(t, "POST", "/jobs", `{}`); r.Status != 403 || r.Err.Code != "FORBIDDEN" {
		t.Fatalf("employee create: %d %+v", r.Status, r.Err)
	}
	if r := e.do(t, "GET", "/jobs", ""); r.Status != 403 {
		t.Fatalf("employee list: %d", r.Status)
	}
	e.signed = false
	if r := e.do(t, "GET", "/jobs", ""); r.Status != 401 {
		t.Fatalf("signed out: %d", r.Status)
	}
	e.signed = true
	e.dir.err = ErrNoOrganization
	if r := e.do(t, "GET", "/jobs", ""); r.Status != 404 {
		t.Fatalf("no org: %d", r.Status)
	}
}

func TestListAndDelete(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	id := e.create(t, `{"title":"Cook"}`)
	r := e.do(t, "GET", "/jobs?status=draft", "")
	if r.Status != 200 || r.Meta["has_more"] != false {
		t.Fatalf("list: %d %v", r.Status, r.Meta)
	}
	var items []map[string]any
	_ = json.Unmarshal(r.Data, &items)
	if len(items) != 1 {
		t.Fatalf("items: %s", r.Data)
	}
	if r := e.do(t, "GET", "/jobs?status=wat", ""); r.Status != 400 {
		t.Fatalf("bad status: %d", r.Status)
	}
	if r := e.do(t, "DELETE", "/jobs/"+id, ""); r.Status != 200 {
		t.Fatalf("delete: %d %+v", r.Status, r.Err)
	}
	if r := e.do(t, "GET", "/jobs/"+id, ""); r.Status != 404 {
		t.Fatalf("after delete: %d", r.Status)
	}
	empty := e.do(t, "GET", "/jobs", "")
	if string(empty.Data) != "[]" {
		t.Fatalf("empty list should be [], got %s", empty.Data)
	}
}

func TestFormAndPreview(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	e.store.Screening = true
	id := e.create(t, fullBody)
	form := `{"questions":[
	 {"section":"personal_information","type":"short_text","label":"Full name","required":true,"system_key":"full_name","config":{}},
	 {"section":"personal_information","type":"email","label":"Email","required":true,"system_key":"email","config":{}},
	 {"section":"screening","type":"single_choice","label":"Right to work in the UK?","required":true,
	  "config":{"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]},"knockout":{"option_ids":["no"]}}]}`
	r := e.do(t, "PUT", "/jobs/"+id+"/application-form", form)
	if r.Status != 200 || r.Header.Get("ETag") == "" {
		t.Fatalf("put form: %d %s %+v", r.Status, r.Data, r.Err)
	}
	g := e.do(t, "GET", "/jobs/"+id+"/application-form", "")
	if g.Status != 200 || g.field(t, "automated_screening_enabled") != true {
		t.Fatalf("get form: %d %s", g.Status, g.Data)
	}
	pv := e.do(t, "GET", "/jobs/"+id+"/preview", "")
	if pv.Status != 200 || strings.Contains(string(pv.Data), "knockout") || !strings.Contains(string(pv.Data), "Acme Ltd") {
		t.Fatalf("preview: %d %s", pv.Status, pv.Data)
	}
	bad := e.do(t, "PUT", "/jobs/"+id+"/application-form",
		`{"questions":[{"section":"screening","type":"short_text","label":"What is your current salary?","config":{}}]}`)
	if bad.Status != 400 {
		t.Fatalf("pay history question accepted: %d", bad.Status)
	}
}

func TestDepartmentsTemplatesSettingsCatalog(t *testing.T) {
	e := newEnv(rbac.RoleFounder)
	d := e.do(t, "POST", "/hiring/departments", `{"name":"Customer Support"}`)
	if d.Status != 200 || d.field(t, "name") != "Customer Support" {
		t.Fatalf("add dept: %d %+v", d.Status, d.Err)
	}
	if r := e.do(t, "POST", "/hiring/departments", `{"name":""}`); r.Status != 400 {
		t.Fatalf("blank dept: %d", r.Status)
	}
	if r := e.do(t, "GET", "/hiring/departments", ""); r.Status != 200 {
		t.Fatalf("list depts: %d", r.Status)
	}

	id := e.create(t, fullBody)
	tpl := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","name":"Engineering","from_job_id":"`+id+`"}`)
	if tpl.Status != 201 {
		t.Fatalf("template: %d %+v", tpl.Status, tpl.Err)
	}
	if dup := e.do(t, "POST", "/hiring/templates", `{"kind":"job_details","name":"engineering","from_job_id":"`+id+`"}`); dup.Status != 400 {
		t.Fatalf("duplicate template name: %d", dup.Status)
	}
	if l := e.do(t, "GET", "/hiring/templates?kind=job_details", ""); l.Status != 200 {
		t.Fatalf("list templates: %d", l.Status)
	}
	if l := e.do(t, "GET", "/hiring/templates?kind=zzz", ""); l.Status != 400 {
		t.Fatalf("bad kind: %d", l.Status)
	}
	if del := e.do(t, "DELETE", "/hiring/templates/"+tpl.field(t, "id").(string), ""); del.Status != 200 {
		t.Fatalf("delete template: %d", del.Status)
	}

	if r := e.do(t, "PUT", "/hiring/settings", `{"automated_screening_enabled":true}`); r.Status != 403 {
		t.Fatalf("founder toggled screening: %d", r.Status)
	}
	legal := newEnv(rbac.RoleLegal)
	r := legal.do(t, "PUT", "/hiring/settings", `{"automated_screening_enabled":true}`)
	if r.Status != 200 || r.field(t, "automated_screening_enabled") != true {
		t.Fatalf("legal toggle: %d %+v", r.Status, r.Err)
	}
	if c := e.do(t, "GET", "/hiring/catalog", ""); c.Status != 200 || c.field(t, "currencies") == nil {
		t.Fatalf("catalog: %d", c.Status)
	}
	if z := e.do(t, "GET", "/hiring/timezones", ""); z.Status != 200 || len(z.Data) < 100 {
		t.Fatalf("timezones: %d", z.Status)
	}
	if s := e.do(t, "GET", "/hiring/skills?q=go&limit=5", ""); s.Status != 200 {
		t.Fatalf("skills: %d", s.Status)
	}
	if dd := e.do(t, "POST", "/jobs/"+id+"/duplicates/dismiss", ""); dd.Status != 200 {
		t.Fatalf("dismiss: %d", dd.Status)
	}
}

func TestPermissionsFor(t *testing.T) {
	owner := PermissionsFor(nil, true)
	if !owner.Has(rbac.JobCreate) || !owner.Has(rbac.JobSalaryRangeSet) {
		t.Fatal("an owner with no roles must act as founder")
	}
	if PermissionsFor(nil, false).Has(rbac.JobCreate) {
		t.Fatal("a member with no roles must have no permissions")
	}
	multi := PermissionsFor([]string{"line_manager", "legal", "unknown_role"}, false)
	if !multi.Has(rbac.JobViewAssigned) || !multi.Has(rbac.FormAutomatedScreening) || multi.Has(rbac.JobCreate) {
		t.Fatal("roles must combine")
	}
}
