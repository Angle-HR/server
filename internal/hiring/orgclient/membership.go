package orgclient

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/callerresolver"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
)

// ErrUnavailable means org could not be reached or failed. The HTTP layer answers 503 (retryable).
var ErrUnavailable = errors.New("orgclient: org service unavailable")

// MembershipSource resolves a signed-in user's company, roles and permissions. callerresolver.Resolver is the
// database implementation (accounts.* tables); GRPCMembership asks OrgService.ResolveMembership.
type MembershipSource interface {
	Lookup(ctx context.Context, userID string, reg region.Region) (*callerresolver.Membership, error)
}

var _ MembershipSource = (*callerresolver.Resolver)(nil)

// GRPCMembership calls OrgService.ResolveMembership. The permissions are the ones org computed, so the
// default role matrix is not read here.
type GRPCMembership struct {
	Client  OrgClient
	Timeout time.Duration
}

// Lookup implements MembershipSource. A user with no company gets apphttp.ErrNoOrganization.
func (g *GRPCMembership) Lookup(ctx context.Context, userID string, reg region.Region) (*callerresolver.Membership, error) {
	if reg == region.RegionGlobal {
		return nil, callerresolver.ErrOnboardingIncomplete
	}
	ctx, cancel := context.WithTimeout(rpc.WithCallContext(ctx, &commonv1.CallContext{
		Region: string(reg), UserId: userID, ActorType: commonv1.ActorType_ACTOR_TYPE_USER,
	}), orDefault(g.Timeout))
	defer cancel()
	resp, err := g.Client.ResolveMembership(ctx, &orgv1.ResolveMembershipRequest{UserId: userID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, apphttp.ErrNoOrganization
		}
		return nil, fmt.Errorf("%w: resolve membership: %v", ErrUnavailable, err)
	}
	if !resp.GetHasCompany() {
		return nil, apphttp.ErrNoOrganization
	}
	perms := make([]rbac.Permission, 0, len(resp.GetPermissions()))
	for _, p := range resp.GetPermissions() {
		perms = append(perms, rbac.Permission(p))
	}
	return &callerresolver.Membership{
		OrgID: resp.GetCompanyId(), CompanyName: resp.GetLegalName(), CompanyAddress: resp.GetRegisteredAddress(),
		Owner: resp.GetIsOwner(), Roles: resp.GetRoles(), Perms: rbac.NewSet(perms...),
	}, nil
}
