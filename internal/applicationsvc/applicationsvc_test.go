package applicationsvc

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/appstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
)

const (
	tCompany = "11111111-1111-4111-8111-111111111111"
	tJob     = "22222222-2222-4222-8222-222222222222"
	tApp     = "33333333-3333-4333-8333-333333333333"
	tUser    = "55555555-5555-4555-8555-555555555555"
)

// ---- fakes ----

type fakeJobs struct{ appjobs.RegionalJobs }

func (fakeJobs) JobAccess(context.Context, string, string, string) (*appjobs.JobAccess, error) {
	return &appjobs.JobAccess{JobID: tJob, CreatedBy: tUser, RetentionMonths: 6}, nil
}
func (fakeJobs) JobSummary(context.Context, string, string) (*appjobs.JobSummary, error) {
	return &appjobs.JobSummary{Code: "JB-1", Title: "Engineer", Company: "Acme", PublicID: "abc", RetentionMonths: 6}, nil
}

type fakeStore struct {
	status     string
	settings   [2]any
	lastChange *applications.Change
	listErr    error
}

func (s *fakeStore) List(_ context.Context, _, _ string, q applications.StoreListQuery) ([]applications.Summary, applications.Counts, error) {
	if s.listErr != nil {
		return nil, applications.Counts{}, s.listErr
	}
	return []applications.Summary{{ID: tApp, Name: "Ada", Email: "ada@example.com", Status: "received", SubmittedAt: time.Unix(1700000000, 0).UTC()}},
		applications.Counts{Status: map[string]int{"received": 1}}, nil
}
func (s *fakeStore) Open(_ context.Context, _ string, in applications.OpenInput) (*applications.Detail, error) {
	return &applications.Detail{
		Summary: applications.Summary{ID: in.AppID, Name: "Ada", Status: "received", SubmittedAt: time.Unix(1700000000, 0).UTC()},
		JobID:   tJob, Answers: []applications.Answer{{QuestionID: "q1"}},
	}, nil
}
func (s *fakeStore) FileInfo(context.Context, string, string, string, string, string) (*applications.FileInfo, error) {
	return nil, applications.ErrNotFound
}
func (s *fakeStore) LoadChange(context.Context, string, string, string) (*applications.ChangeState, error) {
	st := s.status
	if st == "" {
		st = applications.StatusReceived
	}
	return &applications.ChangeState{Status: st}, nil
}
func (s *fakeStore) ApplyChange(_ context.Context, _ string, ch *applications.Change) error {
	s.lastChange = ch
	return nil
}
func (s *fakeStore) GetApplicantSettings(context.Context, string) (int, bool, error) {
	return 0, true, nil
}
func (s *fakeStore) SaveApplicantSettings(_ context.Context, _ string, m int, e bool) error {
	s.settings = [2]any{m, e}
	return nil
}

type fakeLookup struct {
	jobErr error
	counts []appstore.ApplicantCount
}

func (l fakeLookup) JobOfApplication(context.Context, string, string, string) (string, error) {
	return tJob, l.jobErr
}
func (l fakeLookup) ApplicantCounts(context.Context, string, string, []string) ([]appstore.ApplicantCount, error) {
	return l.counts, nil
}

// start runs the server on a bufconn and returns a client whose calls carry cc as CallContext metadata.
func start(t *testing.T, st *fakeStore, lk Lookup) (applicationsv1.ApplicationsServiceClient, func(*commonv1.CallContext) context.Context) {
	t.Helper()
	return startRPC(t, &RPC{
		Applications: func(region.Region) (*applications.Service, error) {
			return &applications.Service{Store: st, Jobs: fakeJobs{}}, nil
		},
		Lookup: lk,
	})
}

func startRPC(t *testing.T, r *RPC) (applicationsv1.ApplicationsServiceClient, func(*commonv1.CallContext) context.Context) {
	t.Helper()
	t.Setenv("GRPC_AUTH", "off") // hermetic: the test client dials plaintext
	lis := bufconn.Listen(1 << 20)
	srv := NewGRPCServer(r)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(rpc.ClientUnaryInterceptor(rpc.Options{})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return applicationsv1.NewApplicationsServiceClient(conn), func(cc *commonv1.CallContext) context.Context {
		return rpc.WithCallContext(context.Background(), cc)
	}
}

func caller(perms ...rbac.Permission) *commonv1.CallContext {
	cc := &commonv1.CallContext{Region: "uk", CompanyId: tCompany, UserId: tUser}
	for _, p := range perms {
		cc.Permissions = append(cc.Permissions, string(p))
	}
	return cc
}

func wantCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("want %v, got %v (%v)", want, status.Code(err), err)
	}
}

// ---- gRPC ----

func TestListApplicationsReadsCallerFromMetadata(t *testing.T) {
	c, ctx := start(t, &fakeStore{}, fakeLookup{})
	res, err := c.ListApplications(ctx(caller(rbac.JobViewAll)), &applicationsv1.ListApplicationsRequest{
		JobId: tJob, Statuses: []applicationsv1.ApplicationStatus{applicationsv1.ApplicationStatus_APPLICATION_STATUS_RECEIVED}, PageSize: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applications) != 1 {
		t.Fatalf("%+v", res)
	}
	a := res.Applications[0]
	if a.Id != tApp || a.JobId != tJob || a.CompanyId != tCompany || a.CandidateEmail != "ada@example.com" ||
		a.Status != applicationsv1.ApplicationStatus_APPLICATION_STATUS_RECEIVED || a.SubmittedAt.AsTime().Unix() != 1700000000 {
		t.Fatalf("%+v", a)
	}
}

func TestListApplicationsNeedsPermissionAndCallContext(t *testing.T) {
	c, ctx := start(t, &fakeStore{}, fakeLookup{})
	req := &applicationsv1.ListApplicationsRequest{JobId: tJob}
	// caller without job.view_all and not the creator or team member
	other := caller(rbac.JobViewAssigned)
	other.UserId = "66666666-6666-4666-8666-666666666666"
	_, err := c.ListApplications(ctx(other), req)
	wantCode(t, err, codes.NotFound) // invisible jobs look missing, as over HTTP
	_, err = c.ListApplications(context.Background(), req)
	wantCode(t, err, codes.Unauthenticated)
	noCompany := caller(rbac.JobViewAll)
	noCompany.CompanyId = ""
	_, err = c.ListApplications(ctx(noCompany), req)
	wantCode(t, err, codes.InvalidArgument)
	global := caller(rbac.JobViewAll)
	global.Region = "global"
	_, err = c.ListApplications(ctx(global), req)
	wantCode(t, err, codes.InvalidArgument)
	_, err = c.ListApplications(ctx(caller(rbac.JobViewAll)), &applicationsv1.ListApplicationsRequest{
		JobId: tJob, Statuses: []applicationsv1.ApplicationStatus{applicationsv1.ApplicationStatus_APPLICATION_STATUS_UNSPECIFIED}})
	wantCode(t, err, codes.InvalidArgument)
}

func TestGetApplicationAndChangeStatus(t *testing.T) {
	st := &fakeStore{}
	c, ctx := start(t, st, fakeLookup{})
	cc := caller(rbac.JobViewAll, rbac.JobManageAny)
	got, err := c.GetApplication(ctx(cc), &applicationsv1.GetApplicationRequest{ApplicationId: tApp})
	if err != nil || got.Application.Id != tApp || got.Application.JobId != tJob || !strings.Contains(got.AnswersJson, "q1") {
		t.Fatalf("%v %+v", err, got)
	}
	res, err := c.ChangeStatus(ctx(cc), &applicationsv1.ChangeStatusRequest{
		ApplicationId: tApp, Status: applicationsv1.ApplicationStatus_APPLICATION_STATUS_IN_REVIEW, Note: "ok"})
	if err != nil || res.Application.Status != applicationsv1.ApplicationStatus_APPLICATION_STATUS_IN_REVIEW {
		t.Fatalf("%v %+v", err, res)
	}
	if st.lastChange == nil || st.lastChange.To != "in_review" || st.lastChange.ActorID != tUser || st.lastChange.JobID != tJob {
		t.Fatalf("%+v", st.lastChange)
	}
	// an invalid transition is FailedPrecondition
	_, err = c.ChangeStatus(ctx(cc), &applicationsv1.ChangeStatusRequest{
		ApplicationId: tApp, Status: applicationsv1.ApplicationStatus_APPLICATION_STATUS_FLAGGED})
	wantCode(t, err, codes.FailedPrecondition)
	_, err = c.ChangeStatus(ctx(cc), &applicationsv1.ChangeStatusRequest{ApplicationId: tApp})
	wantCode(t, err, codes.InvalidArgument)
	_, err = c.GetApplication(ctx(cc), &applicationsv1.GetApplicationRequest{})
	wantCode(t, err, codes.InvalidArgument)
}

func TestUnknownApplicationIsNotFound(t *testing.T) {
	c, ctx := start(t, &fakeStore{}, fakeLookup{jobErr: appstore.ErrNotFound})
	_, err := c.GetApplication(ctx(caller(rbac.JobViewAll)), &applicationsv1.GetApplicationRequest{ApplicationId: tApp})
	wantCode(t, err, codes.NotFound)
}

func TestSettings(t *testing.T) {
	st := &fakeStore{}
	c, ctx := start(t, st, fakeLookup{})
	cc := caller(rbac.JobRetentionSet, rbac.JobViewAll)
	got, err := c.GetSettings(ctx(cc), &applicationsv1.GetSettingsRequest{})
	if err != nil || !got.SendStatusEmails || got.ReapplyBlockMonths != 0 {
		t.Fatalf("%v %+v", err, got)
	}
	off := false
	up, err := c.UpdateSettings(ctx(cc), &applicationsv1.UpdateSettingsRequest{SendStatusEmails: &off})
	if err != nil || up.SendStatusEmails || st.settings[1] != false {
		t.Fatalf("%v %+v %v", err, up, st.settings)
	}
	six := int32(6) // no hasher on this service: cannot turn the block on
	_, err = c.UpdateSettings(ctx(cc), &applicationsv1.UpdateSettingsRequest{ReapplyBlockMonths: &six})
	wantCode(t, err, codes.InvalidArgument)
	_, err = c.UpdateSettings(ctx(caller(rbac.JobViewAll)), &applicationsv1.UpdateSettingsRequest{SendStatusEmails: &off})
	wantCode(t, err, codes.PermissionDenied)
}

func TestApplicantCountsIncludeJobsWithoutApplications(t *testing.T) {
	c, ctx := start(t, &fakeStore{}, fakeLookup{counts: []appstore.ApplicantCount{{JobID: tJob, Total: 7, New: 3}}})
	res, err := c.GetApplicantCounts(ctx(caller()), &applicationsv1.GetApplicantCountsRequest{JobIds: []string{tJob, "other"}})
	if err != nil || len(res.Counts) != 2 || res.Counts[0].Total != 7 || res.Counts[0].NewCount != 3 || res.Counts[1].JobId != "other" || res.Counts[1].Total != 0 {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestGRPCErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{applications.ErrStale, codes.Aborted},
		{applications.ErrNotOpened, codes.FailedPrecondition},
		{applications.ErrNotFound, codes.NotFound},
		{apperror.New(apperror.CodeForbidden, "no"), codes.PermissionDenied},
		{apperror.New(apperror.CodeValidationError, "bad"), codes.InvalidArgument},
		{apperror.New(apperror.CodeServiceUnavailable, "later"), codes.Unavailable},
		{errors.New("pq: password authentication failed"), codes.Internal},
	} {
		got := grpcError(tc.err)
		if status.Code(got) != tc.want {
			t.Errorf("%v: %v", tc.err, got)
		}
		if tc.want == codes.Internal && strings.Contains(got.Error(), "password") {
			t.Error("internal errors must not leak detail")
		}
	}
}

// ---- job source ----

func TestJobSourceFactory(t *testing.T) {
	for _, kind := range []string{"", "db", " DB "} {
		src, err := NewJobSource(kind, JobSourceDeps{})
		if err != nil || src == nil {
			t.Errorf("%q: %v", kind, err)
		}
	}
	if src, err := NewJobSource("events", JobSourceDeps{}); err != nil || src == nil {
		t.Errorf("events: %v", err)
	}
	if _, err := NewJobSource("nope", JobSourceDeps{}); err == nil {
		t.Errorf("unknown kind: %v", err)
	}
}

// ---- HTTP ----

type fakeCallers struct{}

func (fakeCallers) ResolveCaller(_ context.Context, userID string, _ region.Region) (applications.Caller, error) {
	return applications.Caller{UserID: userID, OrgID: tCompany, Perms: rbac.NewSet(rbac.JobViewAll)}, nil
}

func newTestRouter(ready func(context.Context) error) http.Handler {
	return NewRouter(HTTPDeps{
		AllowedOrigins: []string{"http://localhost:3000"},
		Ready:          ready,
		RequireAuth: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") == "" {
					http.Error(w, "no session", http.StatusUnauthorized)
					return
				}
				next.ServeHTTP(w, r)
			})
		},
		Apps: apphttp.Deps{
			Identify: func(context.Context) (string, region.Region, bool) { return tUser, "uk", true },
			Callers:  fakeCallers{},
			Applications: func(region.Region) (*applications.Service, error) {
				return &applications.Service{Store: &fakeStore{}, Jobs: fakeJobs{}}, nil
			},
		},
	})
}

func get(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthAndReadiness(t *testing.T) {
	h := newTestRouter(func(context.Context) error { return nil })
	if rec := get(h, "/healthz", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body)
	}
	rec := get(h, "/readyz", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"nats":"off"`) {
		t.Fatalf("readyz: %d %s", rec.Code, rec.Body)
	}
	down := newTestRouter(func(context.Context) error { return errors.New("db down") })
	if rec = get(down, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with the database down: %d", rec.Code)
	}
	if rec = get(down, "/healthz", nil); rec.Code != 200 {
		t.Fatalf("liveness must not depend on the database: %d", rec.Code)
	}
}

func TestCompanyRoutesNeedASessionPublicRoutesDoNot(t *testing.T) {
	h := newTestRouter(nil)
	if rec := get(h, "/api/v1/jobs/"+tJob+"/applications", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	rec := get(h, "/api/v1/jobs/"+tJob+"/applications", map[string]string{"Cookie": "openhr_session=x"})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), tApp) {
		t.Fatalf("with a session: %d %s", rec.Code, rec.Body)
	}
	// the public apply route is not behind the session: with no apply service it answers 503, not 401
	req := httptest.NewRequest(http.MethodPost, "/api/v1/public/jobs/abc/applications", strings.NewReader(`{}`))
	r2 := httptest.NewRecorder()
	h.ServeHTTP(r2, req)
	if r2.Code != http.StatusServiceUnavailable {
		t.Fatalf("public apply: %d %s", r2.Code, r2.Body)
	}
	// unsafe browser requests from an unknown origin are refused before anything runs
	req = httptest.NewRequest(http.MethodPost, "/api/v1/public/jobs/abc/applications", strings.NewReader(`{}`))
	req.Header.Set("Origin", "https://evil.example")
	r3 := httptest.NewRecorder()
	h.ServeHTTP(r3, req)
	if r3.Code != http.StatusForbidden {
		t.Fatalf("origin guard: %d %s", r3.Code, r3.Body)
	}
}
