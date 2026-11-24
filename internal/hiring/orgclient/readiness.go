// Package orgclient holds hiring's views of the org service: the publish readiness check, caller membership and
// the industry catalog. Each has a database implementation (the interim, default) and a gRPC implementation over
// proto/openhr/org/v1; the process picks one by environment (see hiringsvc).
//
// Publish readiness FAILS CLOSED: when org cannot be reached or answers with an error, the check returns
// draft.ErrCompanyUnavailable, which the HTTP layer reports as 503 SERVICE_UNAVAILABLE (retryable). Nothing is
// published for a company that could not be checked. Drafting never calls it.
package orgclient

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/pkg/rpc"
)

// DefaultTimeout bounds one org call when the caller's context has no earlier deadline.
const DefaultTimeout = 3 * time.Second

// Readiness is what org says about a company's readiness to publish jobs.
type Readiness struct {
	KYBVerified bool
	// KYBStatus is org's status name (verified, in_review ...), informational.
	KYBStatus           string
	DPAAccepted         bool
	PrivacyContactEmail string
	DPOContact          string
}

// ReadinessSource answers the publish readiness question for a company and the agreement version that counts as
// current. An error means "could not tell": callers must not publish.
type ReadinessSource interface {
	PublishReadiness(ctx context.Context, companyID, dpaVersion string) (Readiness, error)
}

// OrgClient is the part of the generated OrgService client this package uses.
type OrgClient interface {
	GetPublishReadiness(ctx context.Context, in *orgv1.GetPublishReadinessRequest, opts ...grpc.CallOption) (*orgv1.GetPublishReadinessResponse, error)
	ResolveMembership(ctx context.Context, in *orgv1.ResolveMembershipRequest, opts ...grpc.CallOption) (*orgv1.ResolveMembershipResponse, error)
	ListCompanies(ctx context.Context, in *orgv1.ListCompaniesRequest, opts ...grpc.CallOption) (*orgv1.ListCompaniesResponse, error)
	ListIndustries(ctx context.Context, in *orgv1.ListIndustriesRequest, opts ...grpc.CallOption) (*orgv1.ListIndustriesResponse, error)
}

var _ OrgClient = orgv1.OrgServiceClient(nil)

// GRPCReadiness calls OrgService.GetPublishReadiness.
type GRPCReadiness struct {
	Client OrgClient
	// Timeout bounds the call; zero means DefaultTimeout.
	Timeout time.Duration
}

// PublishReadiness implements ReadinessSource. Every failure other than "company not found" is
// draft.ErrCompanyUnavailable.
func (g *GRPCReadiness) PublishReadiness(ctx context.Context, companyID, dpaVersion string) (Readiness, error) {
	if g == nil || g.Client == nil {
		return Readiness{}, fmt.Errorf("%w: no org client", draft.ErrCompanyUnavailable)
	}
	ctx, cancel := context.WithTimeout(rpc.WithCallContext(ctx, &commonv1.CallContext{
		Region: draft.RegionFrom(ctx), ActorType: commonv1.ActorType_ACTOR_TYPE_SYSTEM,
	}), orDefault(g.Timeout))
	defer cancel()
	resp, err := g.Client.GetPublishReadiness(ctx, &orgv1.GetPublishReadinessRequest{CompanyId: companyID, DpaVersion: dpaVersion})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return Readiness{}, hiringtypes.ErrNotFound
		}
		slog.WarnContext(ctx, "org readiness call failed", "company_id", companyID, "err", err)
		return Readiness{}, fmt.Errorf("%w: %v", draft.ErrCompanyUnavailable, err)
	}
	if resp == nil {
		return Readiness{}, fmt.Errorf("%w: empty response", draft.ErrCompanyUnavailable)
	}
	return Readiness{
		KYBVerified:         resp.GetKybStatus() == orgv1.KybStatus_KYB_STATUS_VERIFIED,
		KYBStatus:           strings.ToLower(strings.TrimPrefix(resp.GetKybStatus().String(), "KYB_STATUS_")),
		DPAAccepted:         resp.GetDpaAccepted(),
		PrivacyContactEmail: resp.GetPrivacyContactEmail(),
		DPOContact:          resp.GetDpoContact(),
	}, nil
}

func orDefault(d time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return DefaultTimeout
}

// Company implements draft.Company over a ReadinessSource. The DPIA is hiring's own record (hiring.dpia_records),
// so it is read through DPIARecorded, not from org.
type Company struct {
	Source ReadinessSource
	// DPIARecorded reports whether Legal recorded the company DPIA. Nil means not recorded.
	DPIARecorded func(ctx context.Context, companyID string) (bool, error)
	// DPAVersion is the agreement version that counts as current; empty means draft.CurrentDPAVersion.
	DPAVersion string
}

// State implements draft.Company. It fails closed: any error from the source is returned and the publish does
// not proceed (the DPIA lookup is a local database call and fails the same way).
func (c Company) State(ctx context.Context, orgID string) (draft.CompanyState, error) {
	v := c.DPAVersion
	if v == "" {
		v = draft.CurrentDPAVersion
	}
	r, err := c.Source.PublishReadiness(ctx, orgID, v)
	if err != nil {
		return draft.CompanyState{}, err
	}
	st := draft.CompanyState{
		Verified: r.KYBVerified, DPAAccepted: r.DPAAccepted, PrivacyContactSet: strings.TrimSpace(r.PrivacyContactEmail) != "",
	}
	if c.DPIARecorded != nil {
		if st.DPIARecorded, err = c.DPIARecorded(ctx, orgID); err != nil {
			return draft.CompanyState{}, err
		}
	}
	return st, nil
}

var _ draft.Company = Company{}

// Retryable reports whether err is the fail-closed "org could not be checked" error.
func Retryable(err error) bool { return errors.Is(err, draft.ErrCompanyUnavailable) }
