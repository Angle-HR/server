package orgclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/region"
)

// fakeOrg is an OrgService client whose behaviour each test sets.
type fakeOrg struct {
	readiness  func(ctx context.Context, in *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error)
	membership func(ctx context.Context, in *orgv1.ResolveMembershipRequest) (*orgv1.ResolveMembershipResponse, error)
	industries func(ctx context.Context) (*orgv1.ListIndustriesResponse, error)
	companies  func(ctx context.Context, in *orgv1.ListCompaniesRequest) (*orgv1.ListCompaniesResponse, error)
	calls      int
}

func (f *fakeOrg) ListCompanies(ctx context.Context, in *orgv1.ListCompaniesRequest, _ ...grpc.CallOption) (*orgv1.ListCompaniesResponse, error) {
	return f.companies(ctx, in)
}

func (f *fakeOrg) GetPublishReadiness(ctx context.Context, in *orgv1.GetPublishReadinessRequest, _ ...grpc.CallOption) (*orgv1.GetPublishReadinessResponse, error) {
	f.calls++
	return f.readiness(ctx, in)
}
func (f *fakeOrg) ResolveMembership(ctx context.Context, in *orgv1.ResolveMembershipRequest, _ ...grpc.CallOption) (*orgv1.ResolveMembershipResponse, error) {
	return f.membership(ctx, in)
}
func (f *fakeOrg) ListIndustries(ctx context.Context, _ *orgv1.ListIndustriesRequest, _ ...grpc.CallOption) (*orgv1.ListIndustriesResponse, error) {
	f.calls++
	return f.industries(ctx)
}

func company(f *fakeOrg, dpia bool) Company {
	return Company{
		Source:       &GRPCReadiness{Client: f, Timeout: 50 * time.Millisecond},
		DPIARecorded: func(context.Context, string) (bool, error) { return dpia, nil },
	}
}

func TestReadinessReady(t *testing.T) {
	f := &fakeOrg{readiness: func(_ context.Context, in *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
		if in.GetCompanyId() != "co-1" || in.GetDpaVersion() != draft.CurrentDPAVersion {
			t.Errorf("request %v", in)
		}
		return &orgv1.GetPublishReadinessResponse{
			KybStatus: orgv1.KybStatus_KYB_STATUS_VERIFIED, DpaAccepted: true, PrivacyContactEmail: "privacy@acme.test",
		}, nil
	}}
	st, err := company(f, true).State(context.Background(), "co-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (draft.CompanyState{Verified: true, DPAAccepted: true, PrivacyContactSet: true, DPIARecorded: true}); st != want {
		t.Errorf("state %+v, want %+v", st, want)
	}
}

func TestReadinessNotReady(t *testing.T) {
	for name, resp := range map[string]*orgv1.GetPublishReadinessResponse{
		"in review":   {KybStatus: orgv1.KybStatus_KYB_STATUS_IN_REVIEW, DpaAccepted: true, PrivacyContactEmail: "p@x.test"},
		"failed kyb":  {KybStatus: orgv1.KybStatus_KYB_STATUS_FAILED, DpaAccepted: true, PrivacyContactEmail: "p@x.test"},
		"no dpa":      {KybStatus: orgv1.KybStatus_KYB_STATUS_VERIFIED, PrivacyContactEmail: "p@x.test"},
		"blank email": {KybStatus: orgv1.KybStatus_KYB_STATUS_VERIFIED, DpaAccepted: true, PrivacyContactEmail: "  "},
		"unspecified": {},
	} {
		f := &fakeOrg{readiness: func(context.Context, *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
			return resp, nil
		}}
		st, err := company(f, false).State(context.Background(), "co-1")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Verified && st.DPAAccepted && st.PrivacyContactSet {
			t.Errorf("%s: reported ready: %+v", name, st)
		}
	}
}

func TestReadinessDownFailsClosed(t *testing.T) {
	f := &fakeOrg{readiness: func(context.Context, *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}}
	_, err := company(f, true).State(context.Background(), "co-1")
	if !errors.Is(err, draft.ErrCompanyUnavailable) || !Retryable(err) {
		t.Fatalf("down: %v", err)
	}
}

func TestReadinessErrorFailsClosed(t *testing.T) {
	for _, code := range []codes.Code{codes.Internal, codes.Unknown, codes.PermissionDenied, codes.DeadlineExceeded, codes.Unimplemented} {
		f := &fakeOrg{readiness: func(context.Context, *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
			return nil, status.Error(code, "boom")
		}}
		if _, err := company(f, true).State(context.Background(), "co-1"); !errors.Is(err, draft.ErrCompanyUnavailable) {
			t.Errorf("%v: %v", code, err)
		}
	}
}

func TestReadinessSlowTimesOut(t *testing.T) {
	f := &fakeOrg{readiness: func(ctx context.Context, _ *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-time.After(5 * time.Second):
			return &orgv1.GetPublishReadinessResponse{KybStatus: orgv1.KybStatus_KYB_STATUS_VERIFIED}, nil
		}
	}}
	start := time.Now()
	_, err := company(f, true).State(context.Background(), "co-1")
	if !errors.Is(err, draft.ErrCompanyUnavailable) {
		t.Fatalf("slow: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("did not time out: %v", time.Since(start))
	}
}

func TestReadinessNotFoundIsNotFound(t *testing.T) {
	f := &fakeOrg{readiness: func(context.Context, *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
		return nil, status.Error(codes.NotFound, "no such company")
	}}
	if _, err := company(f, true).State(context.Background(), "co-1"); !errors.Is(err, hiringtypes.ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestMembership(t *testing.T) {
	f := &fakeOrg{membership: func(_ context.Context, in *orgv1.ResolveMembershipRequest) (*orgv1.ResolveMembershipResponse, error) {
		switch in.GetUserId() {
		case "u-owner":
			return &orgv1.ResolveMembershipResponse{HasCompany: true, CompanyId: "co-1", LegalName: "Acme", IsOwner: true,
				Roles: []string{"founder"}, Permissions: []string{"job.create"}}, nil
		case "u-none":
			return &orgv1.ResolveMembershipResponse{}, nil
		}
		return nil, status.Error(codes.Unavailable, "down")
	}}
	g := &GRPCMembership{Client: f, Timeout: 50 * time.Millisecond}
	m, err := g.Lookup(context.Background(), "u-owner", region.Region("uk"))
	if err != nil || m.OrgID != "co-1" || !m.Owner || m.CompanyName != "Acme" || !m.Perms.Has("job.create") {
		t.Fatalf("owner: %+v %v", m, err)
	}
	if _, err = g.Lookup(context.Background(), "u-none", region.Region("uk")); !errors.Is(err, apphttp.ErrNoOrganization) {
		t.Errorf("no company: %v", err)
	}
	if _, err = g.Lookup(context.Background(), "u-down", region.Region("uk")); !errors.Is(err, ErrUnavailable) {
		t.Errorf("down: %v", err)
	}
}

type baseRef struct {
	draft.Reference
	industries []hiringtypes.CatalogItem
	checked    string
}

func (b *baseRef) Industries(context.Context) ([]hiringtypes.CatalogItem, error) {
	return b.industries, nil
}
func (b *baseRef) CheckRefs(_ context.Context, _ []string, industry, _, _ string) (*hiringtypes.RefCheck, error) {
	b.checked = industry
	return &hiringtypes.RefCheck{IndustryOK: true, SeniorityOK: true, ExperienceOK: true}, nil
}

func TestIndustriesCacheAndDegrade(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	down := false
	f := &fakeOrg{industries: func(context.Context) (*orgv1.ListIndustriesResponse, error) {
		if down {
			return nil, status.Error(codes.Unavailable, "down")
		}
		return &orgv1.ListIndustriesResponse{Industries: []*orgv1.Industry{{Id: "ind-1", Slug: "tech", Name: "Tech"}}}, nil
	}}
	base := &baseRef{industries: []hiringtypes.CatalogItem{{ID: "ind-db", Name: "From db"}}}
	i := &Industries{Reference: base, Client: f, TTL: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()

	rc, _ := i.CheckRefs(ctx, nil, "ind-1", "", "")
	if !rc.IndustryOK || base.checked != "" {
		t.Errorf("known industry: ok=%v base saw %q", rc.IndustryOK, base.checked)
	}
	if rc, _ = i.CheckRefs(ctx, nil, "ind-x", "", ""); rc.IndustryOK {
		t.Error("unknown industry accepted")
	}
	if f.calls != 1 {
		t.Errorf("org called %d times, want 1 (cached)", f.calls)
	}
	// Org goes down after the TTL: the stale list still answers.
	down = true
	now = now.Add(2 * time.Minute)
	if got, err := i.Industries(ctx); err != nil || len(got) != 1 || got[0].ID != "ind-1" {
		t.Errorf("stale: %v %v", got, err)
	}
	// A cold cache with org down falls back to the database list instead of failing.
	cold := &Industries{Reference: base, Client: f}
	if got, err := cold.Industries(ctx); err != nil || got[0].ID != "ind-db" {
		t.Errorf("cold fallback: %v %v", got, err)
	}
	if rc, _ = cold.CheckRefs(ctx, nil, "ind-1", "", ""); !rc.IndustryOK || base.checked != "ind-1" {
		t.Errorf("cold check delegates to db: %+v %q", rc, base.checked)
	}
}

func TestTenantsPagesThroughCompanies(t *testing.T) {
	pages := map[string]*orgv1.ListCompaniesResponse{
		"":   {Companies: []*orgv1.Company{{Id: "c1"}, {Id: "c2"}}, NextPageToken: "p2"},
		"p2": {Companies: []*orgv1.Company{{Id: "c3"}}},
	}
	f := &fakeOrg{companies: func(_ context.Context, in *orgv1.ListCompaniesRequest) (*orgv1.ListCompaniesResponse, error) {
		return pages[in.GetPageToken()], nil
	}}
	got, err := (&GRPCTenants{Client: f, Region: "uk"}).TenantIDs(context.Background())
	if err != nil || len(got) != 3 || got[2] != "c3" {
		t.Fatalf("%v %v", got, err)
	}
	f.companies = func(context.Context, *orgv1.ListCompaniesRequest) (*orgv1.ListCompaniesResponse, error) {
		return nil, status.Error(codes.Unavailable, "down")
	}
	if _, err = (&GRPCTenants{Client: f, Region: "uk"}).TenantIDs(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("down: %v", err)
	}
}
