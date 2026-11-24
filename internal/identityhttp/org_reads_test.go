package identityhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/response"
)

type fakeOrg struct {
	orgv1.OrgServiceClient
	orgv1.AdminOrgServiceClient
	member   *orgv1.ResolveMembershipResponse
	progress *orgv1.GetOnboardingProgressResponse
	company  *orgv1.GetCompanyByOwnerResponse
	err      error
	region   string
}

func (f *fakeOrg) ResolveMembership(ctx context.Context, _ *orgv1.ResolveMembershipRequest, _ ...grpc.CallOption) (*orgv1.ResolveMembershipResponse, error) {
	return f.member, f.err
}

func (f *fakeOrg) GetOnboardingProgress(ctx context.Context, _ *orgv1.GetOnboardingProgressRequest, _ ...grpc.CallOption) (*orgv1.GetOnboardingProgressResponse, error) {
	return f.progress, f.err
}

func (f *fakeOrg) GetCompanyByOwner(ctx context.Context, _ *orgv1.GetCompanyByOwnerRequest, _ ...grpc.CallOption) (*orgv1.GetCompanyByOwnerResponse, error) {
	return f.company, f.err
}

func newReads(f *fakeOrg) *OrgGRPCReads { return &OrgGRPCReads{Org: f, Admin: f} }

func TestOrgGRPCReads(t *testing.T) {
	ctx := context.Background()
	f := &fakeOrg{
		member: &orgv1.ResolveMembershipResponse{HasCompany: true, Roles: []string{"founder"}, Permissions: []string{"jobs:write", "jobs:read"}},
	}
	roles, perms, err := newReads(f).Membership(ctx, "u1", region.RegionUK)
	if err != nil || len(roles) != 1 || perms[0] != "jobs:read" {
		t.Fatalf("membership = %v %v %v", roles, perms, err)
	}
	// No company and a global account both give no roles, no error.
	f.member = &orgv1.ResolveMembershipResponse{}
	if r, p, err := newReads(f).Membership(ctx, "u1", region.RegionUK); err != nil || r != nil || p != nil {
		t.Fatalf("no company = %v %v %v", r, p, err)
	}
	f.err = status.Error(codes.FailedPrecondition, "onboarding")
	if r, _, err := newReads(f).Membership(ctx, "u1", region.RegionUK); err != nil || r != nil {
		t.Fatalf("failed precondition = %v %v", r, err)
	}
	f.err = status.Error(codes.Unavailable, "down")
	if _, _, err := newReads(f).Membership(ctx, "u1", region.RegionUK); err == nil {
		t.Fatal("unavailable org must be an error")
	}

	f.err = nil
	f.progress = &orgv1.GetOnboardingProgressResponse{Found: true, CurrentStep: "business", CompletedSteps: []string{"profile"}}
	step, done, found, err := newReads(f).Progress(ctx, "u1", region.RegionUK)
	if err != nil || !found || step != "business" || len(done) != 1 {
		t.Fatalf("progress = %v %v %v %v", step, done, found, err)
	}
	f.company = &orgv1.GetCompanyByOwnerResponse{Company: &orgv1.Company{LegalName: "Acme Ltd"}}
	if n, ok, err := newReads(f).OwnedCompanyName(ctx, "u1", region.RegionUK); err != nil || !ok || n != "Acme Ltd" {
		t.Fatalf("company = %q %v %v", n, ok, err)
	}
	f.err = status.Error(codes.NotFound, "none")
	if _, ok, err := newReads(f).OwnedCompanyName(ctx, "u1", region.RegionUK); err != nil || ok {
		t.Fatalf("not found = %v %v", ok, err)
	}
}

func TestOrgReadsFromEnv(t *testing.T) {
	t.Setenv(EnvOrgMembership, "")
	t.Setenv(EnvOrgOnboarding, "")
	if r, closeFn, err := OrgReadsFromEnv(); err != nil || r != nil {
		t.Fatalf("default must stay on the database: %v %v", r, err)
	} else {
		closeFn()
	}
	t.Setenv(EnvOrgMembership, "grpc")
	t.Setenv(EnvOrgGRPCAddr, "")
	if _, _, err := OrgReadsFromEnv(); err == nil {
		t.Fatal("grpc without an address must fail")
	}
	t.Setenv(EnvOrgMembership, "nope")
	if _, _, err := OrgReadsFromEnv(); err == nil {
		t.Fatal("unknown mode must fail")
	}
}

type fakeMembership struct {
	roles, perms []string
	err          error
}

func (f fakeMembership) Membership(context.Context, string, region.Region) ([]string, []string, error) {
	return f.roles, f.perms, f.err
}

func meRequest(t *testing.T, h *AuthHandler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(auth.WithUser(t.Context(), testLoginUserID, region.RegionUK), http.MethodGet, "/auth/me", nil)
	rec := httptest.NewRecorder()
	h.me(rec, req)
	return rec
}

func meHandler(t *testing.T, reads *OrgReads, completed bool) (*AuthHandler, pgxmock.PgxPoolIface) {
	t.Helper()
	regional, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(regional.Close)
	userSQL, userArgs, err := query.LookupAccountUserByID(testLoginUserID)
	if err != nil {
		t.Fatal(err)
	}
	rows := userAccountRows("x", nil)
	if completed {
		rows = pgxmock.NewRows([]string{
			"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at", "account_type", "first_name",
			"last_name", "legal_full_name", "country_id", "totp_secret", "totp_enabled_at",
		}).AddRow(testLoginUserID, testLoginEmail, "x", nil, &testNow, nil, nil, nil, nil, nil, nil, nil)
	}
	regional.ExpectQuery(userSQL).WithArgs(userArgs...).WillReturnRows(rows)
	h := &AuthHandler{
		Router:   dbrouter.NewWithPools(map[region.Region]dbrouter.PgxPool{region.RegionUK: regional}),
		OrgReads: reads,
		MeAccess: func(context.Context, string, region.Region) ([]string, []string) {
			t.Error("MeAccess (database path) must not run when org reads are set")
			return nil, nil
		},
	}
	return h, regional
}

func TestMeMembershipThroughOrg(t *testing.T) {
	h, _ := meHandler(t, &OrgReads{Membership: fakeMembership{roles: []string{"founder"}, perms: []string{"jobs:read"}}}, true)
	rec := meRequest(t, h)
	assertStatus(t, rec, http.StatusOK)
	var env struct{ Data AuthMeData }
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if len(env.Data.Roles) != 1 || env.Data.Roles[0] != "founder" || env.Data.Permissions[0] != "jobs:read" {
		t.Fatalf("data = %+v", env.Data)
	}
}

func TestMeMembershipOrgDownIs503(t *testing.T) {
	h, _ := meHandler(t, &OrgReads{Membership: fakeMembership{err: errors.New("down")}}, true)
	rec := meRequest(t, h)
	assertStatus(t, rec, http.StatusServiceUnavailable)
	var env response.Envelope
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil || env.Error == nil {
		t.Fatalf("want error envelope: %v", err)
	}
}

type fakeOnboarding struct {
	step      string
	completed []string
	found     bool
	err       error
}

func (f fakeOnboarding) Progress(context.Context, string, region.Region) (string, []string, bool, error) {
	return f.step, f.completed, f.found, f.err
}

func (f fakeOnboarding) OwnedCompanyName(context.Context, string, region.Region) (string, bool, error) {
	return "Acme", true, nil
}

func TestMeOnboardingThroughOrg(t *testing.T) {
	h, regional := meHandler(t, &OrgReads{Onboarding: fakeOnboarding{step: "business", completed: []string{"profile"}, found: true}}, false)
	h.MeAccess = nil
	rec := meRequest(t, h)
	assertStatus(t, rec, http.StatusOK)
	var env struct{ Data AuthMeData }
	if err := json.NewDecoder(rec.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Onboarding.CurrentStep == nil || *env.Data.Onboarding.CurrentStep != "business" {
		t.Fatalf("onboarding = %+v", env.Data.Onboarding)
	}
	assertMocksMet(t, regional) // no progress query on the regional database
}

func TestMeOnboardingOrgDownIs503(t *testing.T) {
	h, _ := meHandler(t, &OrgReads{Onboarding: fakeOnboarding{err: errors.New("down")}}, false)
	h.MeAccess = nil
	assertStatus(t, meRequest(t, h), http.StatusServiceUnavailable)
}

var testNow = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }()
