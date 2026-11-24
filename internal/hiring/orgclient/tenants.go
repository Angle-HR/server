package orgclient

import (
	"context"
	"fmt"
	"time"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/pkg/rpc"
)

// GRPCTenants lists a region's companies through OrgService.ListCompanies, for the sweepers (expiry) that walk
// every company. It replaces store.TenantIDs, which reads accounts.organizations.
type GRPCTenants struct {
	Client OrgClient
	// Region is the region whose companies are listed (sent as the CallContext region).
	Region  string
	Timeout time.Duration
}

const tenantPageSize = 500

// TenantIDs pages through all companies of the region.
func (g *GRPCTenants) TenantIDs(ctx context.Context) ([]string, error) {
	ctx = rpc.WithCallContext(ctx, &commonv1.CallContext{Region: g.Region, ActorType: commonv1.ActorType_ACTOR_TYPE_SYSTEM})
	var out []string
	token := ""
	for {
		cctx, cancel := context.WithTimeout(ctx, orDefault(g.Timeout))
		resp, err := g.Client.ListCompanies(cctx, &orgv1.ListCompaniesRequest{PageSize: tenantPageSize, PageToken: token})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%w: list companies: %v", ErrUnavailable, err)
		}
		for _, c := range resp.GetCompanies() {
			out = append(out, c.GetId())
		}
		if token = resp.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}
