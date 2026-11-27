package orgclient

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/pkg/rpc"
)

// DefaultCompanyTTL is how long a company profile is served from memory before org is asked again. After a failed
// refresh the stale entry is served, so the public page and job events do not need org to be up.
const DefaultCompanyTTL = time.Minute

// CompanyClient is the part of OrgService GRPCCompany calls.
type CompanyClient interface {
	GetCompany(ctx context.Context, in *orgv1.GetCompanyRequest, opts ...grpc.CallOption) (*orgv1.GetCompanyResponse, error)
	GetPublishReadiness(ctx context.Context, in *orgv1.GetPublishReadinessRequest, opts ...grpc.CallOption) (*orgv1.GetPublishReadinessResponse, error)
	AcceptAgreement(ctx context.Context, in *orgv1.AcceptAgreementRequest, opts ...grpc.CallOption) (*orgv1.AcceptAgreementResponse, error)
	SetPrivacyContact(ctx context.Context, in *orgv1.SetPrivacyContactRequest, opts ...grpc.CallOption) (*orgv1.SetPrivacyContactResponse, error)
}

var _ CompanyClient = orgv1.OrgServiceClient(nil)

// GRPCCompany implements hiringstore.CompanyOrg over OrgService (ORG_COMPANY=grpc). Profile is cached with a stale
// fallback; Setup and the writes always go to org (the settings page must show what org holds) and fail with
// draft.ErrCompanyUnavailable when org cannot be reached.
type GRPCCompany struct {
	Client  CompanyClient
	Timeout time.Duration
	TTL     time.Duration
	Now     func() time.Time // tests

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	p  hiringstore.CompanyProfile
	at time.Time
}

var _ hiringstore.CompanyOrg = (*GRPCCompany)(nil)

func (g *GRPCCompany) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *GRPCCompany) ctx(ctx context.Context, region, user string) (context.Context, context.CancelFunc) {
	cc := &commonv1.CallContext{Region: region, ActorType: commonv1.ActorType_ACTOR_TYPE_SYSTEM}
	if user != "" {
		cc.UserId, cc.ActorType = user, commonv1.ActorType_ACTOR_TYPE_USER
	}
	return context.WithTimeout(rpc.WithCallContext(ctx, cc), orDefault(g.Timeout))
}

func mapErr(op string, err error) error {
	if status.Code(err) == codes.NotFound {
		return hiringstore.ErrNotFound
	}
	return fmt.Errorf("%w: %s: %v", draft.ErrCompanyUnavailable, op, err)
}

// Profile implements hiringstore.CompanyOrg.
func (g *GRPCCompany) Profile(ctx context.Context, region, tenantID string) (hiringstore.CompanyProfile, error) {
	ttl := g.TTL
	if ttl <= 0 {
		ttl = DefaultCompanyTTL
	}
	g.mu.Lock()
	c, ok := g.cache[tenantID]
	g.mu.Unlock()
	if ok && g.now().Sub(c.at) < ttl {
		return c.p, nil
	}
	cctx, cancel := g.ctx(ctx, region, "")
	defer cancel()
	resp, err := g.Client.GetCompany(cctx, &orgv1.GetCompanyRequest{CompanyId: tenantID})
	if err != nil {
		if ok && status.Code(err) != codes.NotFound {
			return c.p, nil // stale beats nothing
		}
		return hiringstore.CompanyProfile{}, mapErr("get company", err)
	}
	co := resp.GetCompany()
	p := hiringstore.CompanyProfile{
		Name: co.GetLegalName(), PrivacyEmail: co.GetPrivacyContactEmail(), DPOContact: co.GetDpoContact(),
		Slug: co.GetSlug(), DisplayName: co.GetDisplayName(), About: co.GetAbout(), Website: co.GetWebsite(), LogoURL: co.GetLogoUrl(),
	}
	g.put(tenantID, p)
	return p, nil
}

func (g *GRPCCompany) put(tenantID string, p hiringstore.CompanyProfile) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cache == nil {
		g.cache = map[string]cached{}
	}
	g.cache[tenantID] = cached{p: p, at: g.now()}
}

func (g *GRPCCompany) forget(tenantID string) {
	g.mu.Lock()
	delete(g.cache, tenantID)
	g.mu.Unlock()
}

// Setup implements hiringstore.CompanyOrg through OrgService.GetPublishReadiness.
func (g *GRPCCompany) Setup(ctx context.Context, region, tenantID, dpaVersion string) (hiringstore.CompanyReadiness, error) {
	cctx, cancel := g.ctx(ctx, region, "")
	defer cancel()
	r, err := g.Client.GetPublishReadiness(cctx, &orgv1.GetPublishReadinessRequest{CompanyId: tenantID, DpaVersion: dpaVersion})
	if err != nil {
		return hiringstore.CompanyReadiness{}, mapErr("publish readiness", err)
	}
	out := hiringstore.CompanyReadiness{
		KYBStatus:    strings.ToLower(strings.TrimPrefix(r.GetKybStatus().String(), "KYB_STATUS_")),
		PrivacyEmail: r.GetPrivacyContactEmail(), DPOContact: r.GetDpoContact(),
	}
	if r.GetDpaAccepted() {
		t := time.Now()
		if ts := r.GetDpaAcceptedAt(); ts != nil {
			t = ts.AsTime()
		}
		out.DPAAcceptedAt = &t
	}
	return out, nil
}

// AcceptAgreement implements hiringstore.CompanyOrg.
func (g *GRPCCompany) AcceptAgreement(ctx context.Context, region, tenantID, userID, version string) error {
	cctx, cancel := g.ctx(ctx, region, userID)
	defer cancel()
	_, err := g.Client.AcceptAgreement(cctx, &orgv1.AcceptAgreementRequest{
		CompanyId: tenantID, AcceptedByUserId: userID, Kind: "terms_dpa", Version: version,
	})
	if err != nil {
		return mapErr("accept agreement", err)
	}
	return nil
}

// SetPrivacyContact implements hiringstore.CompanyOrg and drops the cached profile.
func (g *GRPCCompany) SetPrivacyContact(ctx context.Context, region, tenantID, email, dpo string) error {
	cctx, cancel := g.ctx(ctx, region, "")
	defer cancel()
	_, err := g.Client.SetPrivacyContact(cctx, &orgv1.SetPrivacyContactRequest{CompanyId: tenantID, Email: email, DpoContact: dpo})
	g.forget(tenantID)
	if err != nil {
		return mapErr("set privacy contact", err)
	}
	return nil
}

// UsersClient is the part of IdentityService GRPCUsers calls.
type UsersClient interface {
	BatchGetUsers(ctx context.Context, in *identityv1.BatchGetUsersRequest, opts ...grpc.CallOption) (*identityv1.BatchGetUsersResponse, error)
}

// GRPCUsers implements hiringstore.UserNames over IdentityService.BatchGetUsers (IDENTITY_USERS=grpc).
type GRPCUsers struct {
	Client  UsersClient
	Timeout time.Duration
}

var _ hiringstore.UserNames = (*GRPCUsers)(nil)

const userBatch = 200

// Names implements hiringstore.UserNames. The display name is first and last name, else the legal name, as the SQL.
func (g *GRPCUsers) Names(ctx context.Context, region string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	ctx = rpc.WithCallContext(ctx, &commonv1.CallContext{Region: region, ActorType: commonv1.ActorType_ACTOR_TYPE_SYSTEM})
	for start := 0; start < len(ids); start += userBatch {
		end := min(start+userBatch, len(ids))
		cctx, cancel := context.WithTimeout(ctx, orDefault(g.Timeout))
		resp, err := g.Client.BatchGetUsers(cctx, &identityv1.BatchGetUsersRequest{UserIds: ids[start:end], ExcludeInactive: true})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("%w: batch get users: %v", ErrUnavailable, err)
		}
		for _, u := range resp.GetUsers() {
			name := strings.TrimSpace(u.GetFirstName() + " " + u.GetLastName())
			if name == "" {
				name = u.GetLegalFullName()
			}
			out[u.GetId()] = name
		}
	}
	return out, nil
}
