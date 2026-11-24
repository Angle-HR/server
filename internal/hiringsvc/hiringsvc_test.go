package hiringsvc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	hiringv1 "github.com/Angle-HR/server/gen/openhr/hiring/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/drafttest"
	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
)

const (
	co   = "bbbbbbbb-0000-4000-8000-000000000001"
	user = "aaaaaaaa-0000-4000-8000-000000000001"
	dept = "44444444-4444-4444-8444-444444444444"
)

type company struct {
	state draft.CompanyState
	err   error
}

func (c *company) State(context.Context, string) (draft.CompanyState, error) { return c.state, c.err }

func newRPC(t *testing.T) (*RPC, *company) {
	t.Helper()
	st := drafttest.NewStore()
	ref := &drafttest.Ref{Skills: map[string]string{}, Industry: "33333333-3333-4333-8333-333333333333"}
	ref.GateList = []gates.Gate{{ID: "uk-lawful", MarketCode: "UK", Severity: gates.SeverityRequired, Requirement: "x", Version: 1,
		Trigger: json.RawMessage(`{"type":"always"}`)}}
	c := &company{state: draft.CompanyState{Verified: true, DPAAccepted: true, PrivacyContactSet: true}}
	n := 0
	svc := &draft.Service{Store: st, Ref: ref, Company: c, Now: func() time.Time { return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC) },
		NewPublicID: func() string { n++; return "jpub" + string(rune('a'+n-1)) }}
	return &RPC{Service: func(region.Region) (*draft.Service, error) { return svc, nil }}, c
}

func callCtx(perms ...rbac.Permission) context.Context {
	var ps []string
	for _, p := range perms {
		ps = append(ps, string(p))
	}
	return rpc.WithCallContext(context.Background(), &commonv1.CallContext{
		Region: "uk", CompanyId: co, UserId: user, Permissions: ps,
	})
}

func founderPerms() []rbac.Permission { return rbac.DefaultMatrix[rbac.RoleFounder] }

const body = `{"title":"Senior Go Engineer","department_id":"` + dept + `","location_mode":"specific_area",
 "markets":[{"market_code":"UK"}],"travel_frequency":"none","visa_sponsorship":"no","employment_type":"full_time",
 "pay":{"type":"range","currency":"GBP","period":"year","min":50000,"max":70000,"visible":true},"lawful_basis":"contract"}`

func TestCallContextIsRequired(t *testing.T) {
	r, _ := newRPC(t)
	for name, ctx := range map[string]context.Context{
		"none":       context.Background(),
		"no company": rpc.WithCallContext(context.Background(), &commonv1.CallContext{Region: "uk", UserId: user}),
		"global":     rpc.WithCallContext(context.Background(), &commonv1.CallContext{Region: "global", UserId: user, CompanyId: co}),
	} {
		if _, err := r.GetJob(ctx, &hiringv1.GetJobRequest{JobId: "x"}); status.Code(err) != codes.Unauthenticated && status.Code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDraftPublishPauseResumeClose(t *testing.T) {
	r, c := newRPC(t)
	ctx := callCtx(founderPerms()...)
	created, err := r.CreateDraft(ctx, &hiringv1.CreateDraftRequest{DetailsJson: body})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Job.Id
	if created.Job.Status != hiringv1.JobStatus_JOB_STATUS_DRAFT || created.Job.CompanyId != co {
		t.Fatalf("draft: %+v", created.Job)
	}
	// Blocked: gate not confirmed. Published=false with the blocking gate, not an error.
	pub, err := r.PublishJob(ctx, &hiringv1.PublishJobRequest{JobId: id})
	if err != nil || pub.Published || len(pub.Blocks) == 0 {
		t.Fatalf("blocked publish: %+v %v", pub, err)
	}
	svc, _ := r.Service("uk")
	if _, err = svc.ConfirmGate(context.Background(), draft.Caller{UserID: user, OrgID: co, Perms: rbac.NewSet(founderPerms()...)}, id, "uk-lawful", true, 0); err != nil {
		t.Fatal(err)
	}
	// Org down: Unavailable, nothing published.
	c.err = draft.ErrCompanyUnavailable
	if _, err = r.PublishJob(ctx, &hiringv1.PublishJobRequest{JobId: id}); status.Code(err) != codes.Unavailable {
		t.Fatalf("org down: %v", err)
	}
	if got, _ := r.GetJob(ctx, &hiringv1.GetJobRequest{JobId: id}); got.Job.Status != hiringv1.JobStatus_JOB_STATUS_DRAFT {
		t.Fatalf("status after failed publish: %v", got.Job.Status)
	}
	c.err = nil
	pub, err = r.PublishJob(ctx, &hiringv1.PublishJobRequest{JobId: id})
	if err != nil || !pub.Published || pub.Job.PublicId == "" {
		t.Fatalf("publish: %+v %v", pub, err)
	}
	if p, err := r.PauseJob(ctx, &hiringv1.PauseJobRequest{JobId: id}); err != nil || p.Job.Status != hiringv1.JobStatus_JOB_STATUS_PAUSED {
		t.Fatalf("pause: %v %v", p, err)
	}
	if p, err := r.ResumeJob(ctx, &hiringv1.ResumeJobRequest{JobId: id}); err != nil || p.Job.Status != hiringv1.JobStatus_JOB_STATUS_PUBLISHED {
		t.Fatalf("resume: %v %v", p, err)
	}
	if p, err := r.CloseJob(ctx, &hiringv1.CloseJobRequest{JobId: id}); err != nil || p.Job.Status != hiringv1.JobStatus_JOB_STATUS_CLOSED {
		t.Fatalf("close: %v %v", p, err)
	}
	list, err := r.ListJobs(ctx, &hiringv1.ListJobsRequest{Statuses: []hiringv1.JobStatus{hiringv1.JobStatus_JOB_STATUS_CLOSED}})
	if err != nil || len(list.Jobs) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	snap, err := r.GetJobsSnapshot(ctx, &hiringv1.GetJobsSnapshotRequest{JobIds: []string{id, "bbbbbbbb-0000-4000-8000-0000000000ff"}})
	if err != nil || len(snap.Jobs) != 1 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
}

type team struct {
	createdBy, role string
	err             error
}

func (tm team) JobTeam(context.Context, region.Region, string, string, string) (string, string, error) {
	return tm.createdBy, tm.role, tm.err
}

func TestCheckJobAccess(t *testing.T) {
	const job = "cccccccc-0000-4000-8000-000000000001"
	cases := []struct {
		name          string
		perms         []rbac.Permission
		tm            team
		allowed, mgmt bool
	}{
		{"view all", []rbac.Permission{rbac.JobViewAll}, team{createdBy: "other"}, true, false},
		{"manage any", []rbac.Permission{rbac.JobViewAll, rbac.JobManageAny}, team{createdBy: "other"}, true, true},
		{"assigned member", []rbac.Permission{rbac.JobViewAssigned}, team{createdBy: "other", role: "collaborator"}, true, false},
		{"assigned hiring manager", []rbac.Permission{rbac.JobViewAssigned}, team{createdBy: "other", role: "hiring_manager"}, true, true},
		{"assigned stranger", []rbac.Permission{rbac.JobViewAssigned}, team{createdBy: "other"}, false, false},
		{"creator", []rbac.Permission{rbac.JobViewAssigned}, team{createdBy: user}, true, true},
		{"no permission", nil, team{createdBy: user}, false, false},
		{"unknown job", []rbac.Permission{rbac.JobViewAll}, team{err: hiringtypes.ErrNotFound}, false, false},
	}
	for _, tc := range cases {
		r := &RPC{Access: tc.tm}
		got, err := r.CheckJobAccess(callCtx(tc.perms...), &hiringv1.CheckJobAccessRequest{JobId: job})
		if err != nil || got.Allowed != tc.allowed || got.CanManage != tc.mgmt {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
	}
}

func TestStatusEnumsRoundTrip(t *testing.T) {
	for e, slug := range statusByEnum {
		if got, ok := statusSlug(e); !ok || got != slug || statusEnum(slug) != e {
			t.Errorf("%v <-> %s", e, slug)
		}
	}
	if _, ok := statusSlug(hiringv1.JobStatus_JOB_STATUS_UNSPECIFIED); ok {
		t.Error("unspecified must not map")
	}
	_ = jobs.StatusDraft
}

func newHTTP(t *testing.T) http.Handler {
	t.Helper()
	dir := &hiringhttp.PostgresHiringDirectory{}
	hh := hiringhttp.NewHiringHandler(dir, func(context.Context) (string, region.Region, bool) { return user, "uk", false })
	hh.NoApplications = true
	ph := hiringhttp.NewPublicHandler(nil, nil)
	ph.NoApplications = true
	return NewRouter(HTTPDeps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Org: map[string]string{"readiness": "grpc"},
		RequireAuth: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauth", http.StatusUnauthorized) })
		},
		Hiring: hh, Public: ph,
	})
}

func TestRouterServesHiringAndNotApplications(t *testing.T) {
	t.Setenv("ORG_REMOTE", "") // the monolith serves the agreements routes unless org is remote
	t.Setenv("APPLICATIONS_REMOTE", "")
	h := newHTTP(t)
	get := func(method, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}
	if c := get("GET", "/healthz"); c != 200 {
		t.Errorf("healthz %d", c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	var ready map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &ready); err != nil || ready["status"] != "ok" || ready["org"] == nil {
		t.Errorf("readyz %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/api/v1/jobs", "/api/v1/jobs/1/publish", "/api/v1/hiring/settings", "/api/v1/organization/agreements"} {
		if c := get("GET", p); c != 401 && c != 405 {
			t.Errorf("%s: %d, want it routed to the auth gate", p, c)
		}
	}
	// The applications routes are the applications service's.
	for _, p := range []struct{ m, path string }{
		{"POST", "/api/v1/public/jobs/jabc/applications"}, {"POST", "/api/v1/public/jobs/jabc/uploads"},
		{"GET", "/api/v1/jobs/1/applications"}, {"GET", "/api/v1/hiring/applicant-settings"},
	} {
		if c := get(p.m, p.path); c != 404 && c != 405 && c != 401 {
			t.Errorf("%s %s: %d", p.m, p.path, c)
		}
		if p.path == "/api/v1/public/jobs/jabc/applications" && get(p.m, p.path) == 200 {
			t.Errorf("apply must not be served here")
		}
	}
}

func TestOrgWiringSwitches(t *testing.T) {
	t.Setenv("GRPC_AUTH", "off")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	w, err := NewOrgWiring(env(nil), log)
	if err != nil || w.Readiness != nil || w.Membership != nil || w.Industries != nil {
		t.Fatalf("default is db: %+v %v", w, err)
	}
	if _, err = NewOrgWiring(env(map[string]string{"ORG_READINESS": "grpc"}), log); err == nil {
		t.Error("grpc without ORG_GRPC_ADDR must fail")
	}
	if _, err = NewOrgWiring(env(map[string]string{"ORG_READINESS": "sometimes"}), log); err == nil {
		t.Error("bad mode must fail")
	}
	w, err = NewOrgWiring(env(map[string]string{"ORG_READINESS": "grpc", "ORG_GRPC_ADDR": "127.0.0.1:1"}), log)
	if err != nil || w.Readiness == nil || w.Membership != nil {
		t.Fatalf("readiness grpc only: %+v %v", w, err)
	}
	defer w.Close()
	// Org unreachable: the lazy connection starts, publish readiness fails closed.
	_, err = w.Readiness.PublishReadiness(context.Background(), co, "v")
	if err == nil {
		t.Fatal("unreachable org must be an error")
	}
}
