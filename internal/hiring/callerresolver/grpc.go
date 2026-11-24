package callerresolver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/codes"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/grpc/status"
)

// Environment variables that select the resolver.
const (
	// EnvResolver is "db" (default: Resolver reads the accounts tables) or "grpc" (GRPC asks the org service).
	EnvResolver = "CALLER_RESOLVER"
	// EnvOrgAddr is the org service's gRPC address, for CALLER_RESOLVER=grpc.
	EnvOrgAddr = "ORG_GRPC_ADDR"
)

// GRPC resolves the caller through OrgService.ResolveMembership. The user and region travel in the pkg/rpc call
// context metadata; the response carries company, owner flag, roles and permissions.
type GRPC struct{ Client orgv1.OrgServiceClient }

// ResolveCaller implements apphttp.CallerResolver. The errors match Resolver: apphttp.ErrNoOrganization when the
// user has no company, ErrOnboardingIncomplete for an account still in the global holding region.
func (g *GRPC) ResolveCaller(ctx context.Context, userID string, reg region.Region) (applications.Caller, error) {
	if reg == region.RegionGlobal {
		return applications.Caller{}, ErrOnboardingIncomplete
	}
	ctx = rpc.WithCallContext(ctx, &commonv1.CallContext{UserId: userID, Region: string(reg)})
	res, err := g.Client.ResolveMembership(ctx, &orgv1.ResolveMembershipRequest{UserId: userID})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return applications.Caller{}, ErrOnboardingIncomplete
		}
		return applications.Caller{}, fmt.Errorf("callerresolver: resolve membership: %w", err)
	}
	if !res.GetHasCompany() {
		return applications.Caller{}, apphttp.ErrNoOrganization
	}
	perms := make([]rbac.Permission, 0, len(res.GetPermissions()))
	for _, p := range res.GetPermissions() {
		perms = append(perms, rbac.Permission(p))
	}
	return applications.Caller{
		UserID: userID, OrgID: res.GetCompanyId(), Region: string(reg), Perms: rbac.NewSet(perms...), Roles: res.GetRoles(),
	}, nil
}

var _ apphttp.CallerResolver = (*GRPC)(nil)

// FromEnv returns the resolver CALLER_RESOLVER selects ("db" by default, "grpc" with ORG_GRPC_ADDR) and a func that
// releases its connection. regional is used by the db resolver.
func FromEnv(regional RegionalDB) (apphttp.CallerResolver, func(), error) {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvResolver))); mode {
	case "", "db":
		return &Resolver{Regional: regional}, func() {}, nil
	case "grpc":
		addr := strings.TrimSpace(os.Getenv(EnvOrgAddr))
		if addr == "" {
			return nil, nil, fmt.Errorf("%s=grpc requires %s", EnvResolver, EnvOrgAddr)
		}
		conn, err := rpc.Dial(addr, "org", rpc.Options{})
		if err != nil {
			return nil, nil, fmt.Errorf("callerresolver: dial org %s: %w", addr, err)
		}
		return &GRPC{Client: orgv1.NewOrgServiceClient(conn)}, func() { _ = conn.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("%s=%q: want db or grpc", EnvResolver, mode)
	}
}
