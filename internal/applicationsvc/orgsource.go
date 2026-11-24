package applicationsvc

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment variables that select where the events job source gets companies from. Both default to the DB
// adapter (the hiring and accounts tables) and need ORG_GRPC_ADDR when "grpc".
const (
	// EnvOrgTenants: "db" (default) or "grpc": the companies of a region for the retention sweep come from
	// OrgService.ListCompanies. Shared with cmd/hiring-expire.
	EnvOrgTenants = "ORG_TENANTS"
	// EnvOrgNotice: "db" (default) or "grpc": the recipient of a retention notice comes from
	// OrgService.GetCompanyContact.
	EnvOrgNotice = "ORG_NOTICE"
	// EnvOrgAddr is the org service's gRPC address.
	EnvOrgAddr = "ORG_GRPC_ADDR"
	// EnvOrgTimeout bounds each org call (default 3s).
	EnvOrgTimeout = "ORG_GRPC_TIMEOUT"
)

const defaultOrgTimeout = 3 * time.Second

// OrgCompanies is the part of OrgService the applications service calls.
type OrgCompanies interface {
	ListCompanies(ctx context.Context, in *orgv1.ListCompaniesRequest, opts ...grpc.CallOption) (*orgv1.ListCompaniesResponse, error)
	GetCompanyContact(ctx context.Context, in *orgv1.GetCompanyContactRequest, opts ...grpc.CallOption) (*orgv1.GetCompanyContactResponse, error)
}

// OrgSource adapts OrgCompanies to the hooks of jobmodel.Model.
type OrgSource struct {
	Client  OrgCompanies
	Timeout time.Duration
}

func (o *OrgSource) ctx(ctx context.Context, region string) (context.Context, context.CancelFunc) {
	t := o.Timeout
	if t <= 0 {
		t = defaultOrgTimeout
	}
	ctx = rpc.WithCallContext(ctx, &commonv1.CallContext{Region: region, ActorType: commonv1.ActorType_ACTOR_TYPE_SYSTEM})
	return context.WithTimeout(ctx, t)
}

// Tenants pages through OrgService.ListCompanies of the region.
func (o *OrgSource) Tenants(ctx context.Context, region string) ([]string, error) {
	var out []string
	token := ""
	for {
		cctx, cancel := o.ctx(ctx, region)
		resp, err := o.Client.ListCompanies(cctx, &orgv1.ListCompaniesRequest{PageSize: 500, PageToken: token})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("applicationsvc: org list companies: %w", err)
		}
		for _, c := range resp.GetCompanies() {
			out = append(out, c.GetId())
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

// Notice calls OrgService.GetCompanyContact.
func (o *OrgSource) Notice(ctx context.Context, region, tenantID string) (string, string, error) {
	cctx, cancel := o.ctx(ctx, region)
	defer cancel()
	resp, err := o.Client.GetCompanyContact(cctx, &orgv1.GetCompanyContactRequest{CompanyId: tenantID})
	if err != nil {
		return "", "", fmt.Errorf("applicationsvc: org company contact: %w", err)
	}
	return resp.GetNoticeEmail(), resp.GetLegalName(), nil
}

// OrgSourceFromEnv returns the org adapter the switches ask for and which hooks to use; nil, false, false when both
// stay on db. The returned func closes the connection.
func OrgSourceFromEnv() (src *OrgSource, tenants, notice bool, closeFn func(), err error) {
	tenants = strings.EqualFold(strings.TrimSpace(os.Getenv(EnvOrgTenants)), "grpc")
	notice = strings.EqualFold(strings.TrimSpace(os.Getenv(EnvOrgNotice)), "grpc")
	for _, k := range []string{EnvOrgTenants, EnvOrgNotice} {
		if v := strings.ToLower(strings.TrimSpace(os.Getenv(k))); v != "" && v != "db" && v != "grpc" {
			return nil, false, false, nil, fmt.Errorf("%s=%q: want db or grpc", k, v)
		}
	}
	if !tenants && !notice {
		return nil, false, false, func() {}, nil
	}
	addr := strings.TrimSpace(os.Getenv(EnvOrgAddr))
	if addr == "" {
		return nil, false, false, nil, fmt.Errorf("%s/%s=grpc requires %s", EnvOrgTenants, EnvOrgNotice, EnvOrgAddr)
	}
	conn, derr := rpc.Dial(addr, "org", rpc.Options{})
	if derr != nil {
		return nil, false, false, nil, fmt.Errorf("applicationsvc: dial org %s: %w", addr, derr)
	}
	timeout := defaultOrgTimeout
	if d, perr := time.ParseDuration(strings.TrimSpace(os.Getenv(EnvOrgTimeout))); perr == nil && d > 0 {
		timeout = d
	}
	return &OrgSource{Client: orgv1.NewOrgServiceClient(conn), Timeout: timeout}, tenants, notice, func() { _ = conn.Close() }, nil
}
