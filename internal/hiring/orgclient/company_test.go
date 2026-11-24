package orgclient

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
)

type fakeCompany struct {
	calls int
	err   error
	name  string
}

func (f *fakeCompany) GetCompany(context.Context, *orgv1.GetCompanyRequest, ...grpc.CallOption) (*orgv1.GetCompanyResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &orgv1.GetCompanyResponse{Company: &orgv1.Company{LegalName: f.name, PrivacyContactEmail: "p@x.test"}}, nil
}
func (f *fakeCompany) GetPublishReadiness(context.Context, *orgv1.GetPublishReadinessRequest, ...grpc.CallOption) (*orgv1.GetPublishReadinessResponse, error) {
	return &orgv1.GetPublishReadinessResponse{KybStatus: orgv1.KybStatus_KYB_STATUS_VERIFIED, DpaAccepted: true}, f.err
}
func (f *fakeCompany) AcceptAgreement(context.Context, *orgv1.AcceptAgreementRequest, ...grpc.CallOption) (*orgv1.AcceptAgreementResponse, error) {
	return &orgv1.AcceptAgreementResponse{}, f.err
}
func (f *fakeCompany) SetPrivacyContact(context.Context, *orgv1.SetPrivacyContactRequest, ...grpc.CallOption) (*orgv1.SetPrivacyContactResponse, error) {
	return &orgv1.SetPrivacyContactResponse{}, f.err
}

func TestGRPCCompanyProfileCacheAndStale(t *testing.T) {
	now := time.Now()
	f := &fakeCompany{name: "Acme"}
	g := &GRPCCompany{Client: f, TTL: time.Minute, Now: func() time.Time { return now }}
	ctx := context.Background()
	if p, err := g.Profile(ctx, "uk", "c1"); err != nil || p.Name != "Acme" {
		t.Fatalf("%+v %v", p, err)
	}
	g.Profile(ctx, "uk", "c1")
	if f.calls != 1 {
		t.Fatalf("second read must be cached, calls=%d", f.calls)
	}
	now = now.Add(2 * time.Minute)
	f.err = status.Error(codes.Unavailable, "down")
	if p, err := g.Profile(ctx, "uk", "c1"); err != nil || p.Name != "Acme" {
		t.Fatalf("stale entry expected: %+v %v", p, err)
	}
	if _, err := g.Profile(ctx, "uk", "never-seen"); !errors.Is(err, draft.ErrCompanyUnavailable) {
		t.Fatalf("cold cache + org down: %v", err)
	}
	f.err = status.Error(codes.NotFound, "no")
	if _, err := g.Profile(ctx, "uk", "c2"); !errors.Is(err, hiringstore.ErrNotFound) {
		t.Fatalf("not found: %v", err)
	}
	// A privacy contact change drops the cached profile.
	f.err = nil
	g.Profile(ctx, "uk", "c3")
	before := f.calls
	if err := g.SetPrivacyContact(ctx, "uk", "c3", "n@x.test", ""); err != nil {
		t.Fatal(err)
	}
	g.Profile(ctx, "uk", "c3")
	if f.calls != before+1 {
		t.Fatal("profile not refreshed after SetPrivacyContact")
	}
	if r, err := g.Setup(ctx, "uk", "c3", "v"); err != nil || r.KYBStatus != "verified" || r.DPAAcceptedAt == nil {
		t.Fatalf("setup %+v %v", r, err)
	}
}

type fakeUsers struct{}

func (fakeUsers) BatchGetUsers(_ context.Context, in *identityv1.BatchGetUsersRequest, _ ...grpc.CallOption) (*identityv1.BatchGetUsersResponse, error) {
	out := &identityv1.BatchGetUsersResponse{}
	for _, id := range in.UserIds {
		if id == "gone" {
			continue
		}
		out.Users = append(out.Users, &identityv1.User{Id: id, FirstName: "Ada", LastName: "L"})
	}
	return out, nil
}

func TestGRPCUsersNames(t *testing.T) {
	m, err := (&GRPCUsers{Client: fakeUsers{}}).Names(context.Background(), "uk", []string{"a", "gone"})
	if err != nil || m["a"] != "Ada L" || len(m) != 1 {
		t.Fatalf("%v %v", m, err)
	}
}
