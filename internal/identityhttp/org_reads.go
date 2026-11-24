package identityhttp

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment variables that select where identity reads company data from.
const (
	// EnvOrgMembership is "db" (default: the monolith/identity read accounts tables) or "grpc" (OrgService.ResolveMembership)
	// for the company roles and permissions on /auth/me.
	EnvOrgMembership = "ORG_MEMBERSHIP"
	// EnvOrgOnboarding is "db" (default) or "grpc" (OrgService.GetOnboardingProgress, AdminOrgService.GetCompanyByOwner)
	// for the onboarding progress on /auth/me and the progress and company name on the admin user detail.
	EnvOrgOnboarding = "ORG_ONBOARDING"
	envOrgTimeout    = "ORG_GRPC_TIMEOUT"
	defaultOrgTO     = 3 * time.Second
)

// OrgReads is how identity reads company data it does not own. A nil field (or a nil *OrgReads) keeps the interim
// read of org's accounts tables. With gRPC, a failing org answers 503 on /auth/me and leaves the admin detail
// enrichment out (logged); there is no fallback to the database.
type OrgReads struct {
	// Membership returns the caller's roles and permissions (empty, nil error, for a user without a company).
	Membership interface {
		Membership(ctx context.Context, userID string, reg region.Region) (roles, permissions []string, err error)
	}
	// Onboarding returns progress and the name of the company a user owns.
	Onboarding interface {
		Progress(ctx context.Context, userID string, reg region.Region) (step string, completed []string, found bool, err error)
		OwnedCompanyName(ctx context.Context, userID string, reg region.Region) (name string, found bool, err error)
	}
}

func (o *OrgReads) membership() interface {
	Membership(ctx context.Context, userID string, reg region.Region) ([]string, []string, error)
} {
	if o == nil {
		return nil
	}
	return o.Membership
}

func (o *OrgReads) onboarding() interface {
	Progress(ctx context.Context, userID string, reg region.Region) (string, []string, bool, error)
	OwnedCompanyName(ctx context.Context, userID string, reg region.Region) (string, bool, error)
} {
	if o == nil {
		return nil
	}
	return o.Onboarding
}

// OrgGRPCReads implements both readers over org's gRPC services.
type OrgGRPCReads struct {
	Org     orgv1.OrgServiceClient
	Admin   orgv1.AdminOrgServiceClient
	Timeout time.Duration
}

func (g *OrgGRPCReads) call(ctx context.Context, userID string, reg region.Region) (context.Context, context.CancelFunc) {
	to := g.Timeout
	if to <= 0 {
		to = defaultOrgTO
	}
	ctx, cancel := context.WithTimeout(ctx, to)
	return rpc.WithCallContext(ctx, &commonv1.CallContext{UserId: userID, Region: string(reg)}), cancel
}

// Membership implements the membership reader through ResolveMembership. An account still in the global holding
// area and a user without a company both yield no roles, as the database path does.
func (g *OrgGRPCReads) Membership(ctx context.Context, userID string, reg region.Region) ([]string, []string, error) {
	if reg == region.RegionGlobal {
		return nil, nil, nil
	}
	ctx, cancel := g.call(ctx, userID, reg)
	defer cancel()
	res, err := g.Org.ResolveMembership(ctx, &orgv1.ResolveMembershipRequest{UserId: userID})
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("org resolve membership: %w", err)
	}
	if !res.GetHasCompany() {
		return nil, nil, nil
	}
	perms := append([]string{}, res.GetPermissions()...)
	sort.Strings(perms)
	return append([]string{}, res.GetRoles()...), perms, nil
}

// Progress reads onboarding progress through GetOnboardingProgress.
func (g *OrgGRPCReads) Progress(ctx context.Context, userID string, reg region.Region) (string, []string, bool, error) {
	ctx, cancel := g.call(ctx, userID, reg)
	defer cancel()
	res, err := g.Org.GetOnboardingProgress(ctx, &orgv1.GetOnboardingProgressRequest{UserId: userID})
	if err != nil {
		return "", nil, false, fmt.Errorf("org onboarding progress: %w", err)
	}
	return res.GetCurrentStep(), res.GetCompletedSteps(), res.GetFound(), nil
}

// OwnedCompanyName reads the owned company through GetCompanyByOwner.
func (g *OrgGRPCReads) OwnedCompanyName(ctx context.Context, userID string, reg region.Region) (string, bool, error) {
	ctx, cancel := g.call(ctx, userID, reg)
	defer cancel()
	res, err := g.Admin.GetCompanyByOwner(ctx, &orgv1.GetCompanyByOwnerRequest{OwnerUserId: userID})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return "", false, nil
		}
		return "", false, fmt.Errorf("org company by owner: %w", err)
	}
	return res.GetCompany().GetLegalName(), true, nil
}

// OrgReadsFromEnv builds the OrgReads selected by ORG_MEMBERSHIP and ORG_ONBOARDING (both "db" by default, which
// returns nil). The returned func releases the connection.
func OrgReadsFromEnv() (*OrgReads, func(), error) {
	mode := func(name string) (string, error) {
		m := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
		if m != "" && m != "db" && m != "grpc" {
			return "", fmt.Errorf("%s=%q: want db or grpc", name, m)
		}
		return m, nil
	}
	mem, err := mode(EnvOrgMembership)
	if err != nil {
		return nil, nil, err
	}
	onb, err := mode(EnvOrgOnboarding)
	if err != nil {
		return nil, nil, err
	}
	if mem != "grpc" && onb != "grpc" {
		return nil, func() {}, nil
	}
	addr := strings.TrimSpace(os.Getenv(EnvOrgGRPCAddr))
	if addr == "" {
		return nil, nil, fmt.Errorf("%s=grpc / %s=grpc require %s", EnvOrgMembership, EnvOrgOnboarding, EnvOrgGRPCAddr)
	}
	conn, err := rpc.Dial(addr, "org", rpc.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("org grpc client: %w", err)
	}
	to := defaultOrgTO
	if v := strings.TrimSpace(os.Getenv(envOrgTimeout)); v != "" {
		if d, perr := time.ParseDuration(v); perr == nil && d > 0 {
			to = d
		}
	}
	g := &OrgGRPCReads{Org: orgv1.NewOrgServiceClient(conn), Admin: orgv1.NewAdminOrgServiceClient(conn), Timeout: to}
	out := &OrgReads{}
	if mem == "grpc" {
		out.Membership = g
	}
	if onb == "grpc" {
		out.Onboarding = g
	}
	return out, func() { _ = conn.Close() }, nil
}
