package callerresolver

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rbac"
)

type fakeOrg struct {
	orgv1.OrgServiceClient
	res *orgv1.ResolveMembershipResponse
	err error
}

func (f fakeOrg) ResolveMembership(context.Context, *orgv1.ResolveMembershipRequest, ...grpc.CallOption) (*orgv1.ResolveMembershipResponse, error) {
	return f.res, f.err
}

func TestGRPCResolver(t *testing.T) {
	ctx := context.Background()
	g := &GRPC{Client: fakeOrg{res: &orgv1.ResolveMembershipResponse{
		HasCompany: true, CompanyId: "c1", Roles: []string{"founder"}, Permissions: []string{string(rbac.JobCreate)},
	}}}
	c, err := g.ResolveCaller(ctx, "u1", region.RegionUK)
	if err != nil || c.OrgID != "c1" || !c.Perms.Has(rbac.JobCreate) || c.UserID != "u1" {
		t.Fatalf("%+v %v", c, err)
	}
	if _, err = (&GRPC{Client: fakeOrg{res: &orgv1.ResolveMembershipResponse{}}}).ResolveCaller(ctx, "u", region.RegionUK); !errors.Is(err, apphttp.ErrNoOrganization) {
		t.Fatalf("no company: %v", err)
	}
	if _, err = (&GRPC{Client: fakeOrg{err: status.Error(codes.FailedPrecondition, "x")}}).ResolveCaller(ctx, "u", region.RegionUK); err != ErrOnboardingIncomplete {
		t.Fatalf("precondition: %v", err)
	}
	if _, err = g.ResolveCaller(ctx, "u", region.RegionGlobal); err != ErrOnboardingIncomplete {
		t.Fatalf("global: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvResolver, "grpc")
	t.Setenv(EnvOrgAddr, "")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("grpc without address must fail")
	}
	t.Setenv(EnvResolver, "nope")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("unknown mode must fail")
	}
	t.Setenv(EnvResolver, "")
	r, _, err := FromEnv(nil)
	if _, ok := r.(*Resolver); err != nil || !ok {
		t.Fatalf("default is db: %v", err)
	}
}
