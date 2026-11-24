package orgsvc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/regionsaga"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Catalog reads the global onboarding catalogs.
type Catalog interface {
	Industries(ctx context.Context) ([]Industry, error)
	IndustryExists(ctx context.Context, slug string) (bool, error)
}

// RegionMover is the region-migration saga (regionsaga.Coordinator).
type RegionMover interface {
	Move(ctx context.Context, userID uuid.UUID, target region.Region) (region.Region, error)
}

// CountryResolver says which region serves a country.
type CountryResolver interface {
	CountryRegion(ctx context.Context, countryID uuid.UUID) (region.Region, error)
}

// Industry is one onboarding industry.
type Industry struct{ ID, Slug, Name string }

// KYBReviewer applies a reviewer's decision (kyb.Service.Review over the company's region).
type KYBReviewer interface {
	Review(ctx context.Context, reg region.Region, companyID, reviewerID string, approve bool, reason kyb.FailureReason) (*kyb.Record, error)
	Status(ctx context.Context, reg region.Region, companyID string) (*kyb.Record, error)
}

// RPC implements openhr.org.v1.OrgService and AdminOrgService.
//
// The caller's region (and user) come from the pkg/rpc CallContext metadata, so the server must be built with
// rpc.ServerUnaryInterceptor: a company id alone does not say which regional database holds it.
//
// Implemented: ResolveMembership, GetCompany, GetCompanyContact, GetPublishReadiness, ListMembers, AddMember,
// SetMemberRoles, AcceptAgreement, SetPrivacyContact, ListCompanies, ListIndustries, ValidateIndustry,
// GetKybStatus, and AdminOrgService.ApplyKybDecision, GetCompanyByOwner and DeleteCompany.
//
// CompleteRegionOnboarding runs the region-migration saga (REGION_MOVE=saga, else Unimplemented) and
// AdminOrgService.ReactivateCompany undoes DeleteCompany.
//
// Unimplemented, with the reason in the status message: SubmitKyb (the request has no legal name or address, which
// the verification needs; use the HTTP flow).
type RPC struct {
	orgv1.UnimplementedOrgServiceServer
	orgv1.UnimplementedAdminOrgServiceServer

	Store   *orgstore.Store
	Catalog Catalog
	// Mover and Countries serve CompleteRegionOnboarding (REGION_MOVE=saga); without them it is Unimplemented.
	Mover     RegionMover
	Countries CountryResolver
	KYB       KYBReviewer
	Log       *slog.Logger
	// DPAVersion is the agreement version that counts as current when a request names none.
	DPAVersion string
}

// NewGRPCServer returns a gRPC server with OrgService and AdminOrgService registered and the CallContext
// interceptor installed.
func NewGRPCServer(r *RPC, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(append(rpc.ServerOptions("org", orgAllow), grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{}))), opts...)
	s := grpc.NewServer(opts...)
	orgv1.RegisterOrgServiceServer(s, r)
	orgv1.RegisterAdminOrgServiceServer(s, r)
	return s
}

func callRegion(ctx context.Context) (region.Region, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	reg := region.Region(cc.GetRegion())
	if reg == region.RegionGlobal {
		return "", status.Error(codes.FailedPrecondition, "onboarding is not finished: the account has no region yet")
	}
	if !region.Valid(reg) {
		return "", status.Errorf(codes.InvalidArgument, "call context has no usable region (x-openhr-region=%q)", cc.GetRegion())
	}
	return reg, nil
}

func grpcError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, orgstore.ErrNotFound), errors.Is(err, orgstore.ErrNoCompany):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, orgstore.ErrInvalid), errors.Is(err, kyb.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, kyb.ErrActionNotAllowed):
		return status.Error(codes.FailedPrecondition, "no pending review for this company")
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, "internal error")
}

func (s *RPC) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *RPC) internal(op string, err error) error {
	out := grpcError(err)
	if status.Code(out) == codes.Internal {
		s.log().Error("org rpc failed", "op", op, "error", err)
	}
	return out
}

func kybStatus(st string) orgv1.KybStatus {
	switch kyb.Status(st) {
	case kyb.StatusNotStarted:
		return orgv1.KybStatus_KYB_STATUS_NOT_STARTED
	case kyb.StatusPending:
		return orgv1.KybStatus_KYB_STATUS_PENDING
	case kyb.StatusVerified:
		return orgv1.KybStatus_KYB_STATUS_VERIFIED
	case kyb.StatusFailed:
		return orgv1.KybStatus_KYB_STATUS_FAILED
	}
	return orgv1.KybStatus_KYB_STATUS_UNSPECIFIED
}

func companyMsg(c *orgstore.Company) *orgv1.Company {
	return &orgv1.Company{
		Id: c.ID, LegalName: c.LegalName, RegisteredAddress: c.Address, OwnerUserId: c.OwnerUserID,
		KybStatus: kybStatus(c.KYBStatus), PrivacyContactEmail: c.PrivacyEmail, DpoContact: c.DPOContact, Region: c.Region,
	}
}

// ResolveMembership answers which company the user acts for and with which permissions. A user with no company
// gets has_company=false; an account still in the global holding region gets FailedPrecondition.
func (s *RPC) ResolveMembership(ctx context.Context, req *orgv1.ResolveMembershipRequest) (*orgv1.ResolveMembershipResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	m, err := s.Store.Resolve(ctx, reg, req.GetUserId())
	if errors.Is(err, orgstore.ErrNoCompany) {
		return &orgv1.ResolveMembershipResponse{}, nil
	}
	if err != nil {
		return nil, s.internal("ResolveMembership", err)
	}
	return &orgv1.ResolveMembershipResponse{
		HasCompany: true, CompanyId: m.CompanyID, LegalName: m.LegalName, RegisteredAddress: m.Address,
		IsOwner: m.IsOwner, Roles: m.Roles, Permissions: orgstore.PermissionList(m.Perms),
	}, nil
}

// GetOnboardingProgress returns the user's onboarding step and completed steps (found=false when no row exists).
func (s *RPC) GetOnboardingProgress(ctx context.Context, req *orgv1.GetOnboardingProgressRequest) (*orgv1.GetOnboardingProgressResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}
	p, err := s.Store.Progress(ctx, reg, req.GetUserId())
	if errors.Is(err, orgstore.ErrNotFound) {
		return &orgv1.GetOnboardingProgressResponse{}, nil
	}
	if err != nil {
		return nil, s.internal("GetOnboardingProgress", err)
	}
	return &orgv1.GetOnboardingProgressResponse{Found: true, CurrentStep: p.CurrentStep, CompletedSteps: p.CompletedSteps}, nil
}

// GetCompany returns the company row.
func (s *RPC) GetCompany(ctx context.Context, req *orgv1.GetCompanyRequest) (*orgv1.GetCompanyResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.Store.Company(ctx, reg, req.GetCompanyId())
	if err != nil {
		return nil, s.internal("GetCompany", err)
	}
	return &orgv1.GetCompanyResponse{Company: companyMsg(c)}, nil
}

// GetCompanyContact returns where notices go: the privacy contact, else the owner's email.
func (s *RPC) GetCompanyContact(ctx context.Context, req *orgv1.GetCompanyContactRequest) (*orgv1.GetCompanyContactResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.Store.Contact(ctx, reg, req.GetCompanyId())
	if err != nil {
		return nil, s.internal("GetCompanyContact", err)
	}
	return &orgv1.GetCompanyContactResponse{LegalName: c.LegalName, NoticeEmail: c.NoticeEmail}, nil
}

// GetPublishReadiness is the company-side input to the publish gate, read from the company's region.
func (s *RPC) GetPublishReadiness(ctx context.Context, req *orgv1.GetPublishReadinessRequest) (*orgv1.GetPublishReadinessResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	version := req.GetDpaVersion()
	if version == "" {
		version = s.DPAVersion
	}
	r, err := s.Store.Readiness(ctx, reg, req.GetCompanyId(), version)
	if err != nil {
		return nil, s.internal("GetPublishReadiness", err)
	}
	out := &orgv1.GetPublishReadinessResponse{
		KybStatus: kybStatus(r.KYBStatus), DpaAccepted: r.DPAAccepted,
		PrivacyContactEmail: r.PrivacyEmail, DpoContact: r.DPOContact,
	}
	if r.DPAAcceptedA != nil {
		out.DpaAcceptedAt = timestamppb.New(*r.DPAAcceptedA)
	}
	return out, nil
}

// ListMembers lists the company's members, or those named in user_ids.
func (s *RPC) ListMembers(ctx context.Context, req *orgv1.ListMembersRequest) (*orgv1.ListMembersResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := s.Store.ListMembers(ctx, reg, req.GetCompanyId(), req.GetUserIds())
	if err != nil {
		return nil, s.internal("ListMembers", err)
	}
	out := &orgv1.ListMembersResponse{}
	for _, m := range ms {
		out.Members = append(out.Members, &orgv1.CompanyMember{UserId: m.UserID, Roles: m.Roles, IsOwner: m.IsOwner})
	}
	return out, nil
}

// AddMember adds a user to a company (identity calls it when an org invite is accepted).
func (s *RPC) AddMember(ctx context.Context, req *orgv1.AddMemberRequest) (*orgv1.AddMemberResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	id, err := s.Store.AddMember(ctx, reg, req.GetCompanyId(), req.GetUserId(), req.GetRoles())
	if err != nil {
		return nil, s.internal("AddMember", err)
	}
	return &orgv1.AddMemberResponse{MemberId: id}, nil
}

// SetMemberRoles replaces a member's roles.
func (s *RPC) SetMemberRoles(ctx context.Context, req *orgv1.SetMemberRolesRequest) (*orgv1.SetMemberRolesResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	if err = s.Store.SetMemberRoles(ctx, reg, req.GetCompanyId(), req.GetUserId(), req.GetRoles()); err != nil {
		return nil, s.internal("SetMemberRoles", err)
	}
	ms, err := s.Store.ListMembers(ctx, reg, req.GetCompanyId(), []string{req.GetUserId()})
	if err != nil || len(ms) == 0 {
		return &orgv1.SetMemberRolesResponse{}, s.internal("SetMemberRoles", err)
	}
	return &orgv1.SetMemberRolesResponse{Member: &orgv1.CompanyMember{UserId: ms[0].UserID, Roles: ms[0].Roles, IsOwner: ms[0].IsOwner}}, nil
}

// AcceptAgreement records the acceptance of an agreement version and emits org.agreement.accepted.v1.
func (s *RPC) AcceptAgreement(ctx context.Context, req *orgv1.AcceptAgreementRequest) (*orgv1.AcceptAgreementResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	kind := req.GetKind()
	if kind == "" {
		kind = "terms_dpa"
	}
	if req.GetVersion() == "" || req.GetAcceptedByUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "version and accepted_by_user_id are required")
	}
	at, err := s.Store.AcceptAgreement(ctx, reg, req.GetCompanyId(), req.GetAcceptedByUserId(), kind, req.GetVersion())
	if err != nil {
		return nil, s.internal("AcceptAgreement", err)
	}
	return &orgv1.AcceptAgreementResponse{AcceptedAt: timestamppb.New(at)}, nil
}

// SetPrivacyContact stores the privacy contact and emits org.company.profile_updated.v1.
func (s *RPC) SetPrivacyContact(ctx context.Context, req *orgv1.SetPrivacyContactRequest) (*orgv1.SetPrivacyContactResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetEmail()) == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if err = s.Store.SetPrivacyContact(ctx, reg, req.GetCompanyId(), req.GetEmail(), req.GetDpoContact()); err != nil {
		return nil, s.internal("SetPrivacyContact", err)
	}
	return &orgv1.SetPrivacyContactResponse{}, nil
}

// ListCompanies pages through a region's companies; page_token is the id of the last company of the previous page.
func (s *RPC) ListCompanies(ctx context.Context, req *orgv1.ListCompaniesRequest) (*orgv1.ListCompaniesResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	size := int(req.GetPageSize())
	if size <= 0 || size > 500 {
		size = 100
	}
	cs, err := s.Store.ListCompanies(ctx, reg, req.GetPageToken(), size+1)
	if err != nil {
		return nil, s.internal("ListCompanies", err)
	}
	out := &orgv1.ListCompaniesResponse{}
	if len(cs) > size {
		cs = cs[:size]
		out.NextPageToken = cs[size-1].ID
	}
	for i := range cs {
		out.Companies = append(out.Companies, companyMsg(&cs[i]))
	}
	return out, nil
}

// ListIndustries lists the active onboarding industries (global catalog).
func (s *RPC) ListIndustries(ctx context.Context, _ *orgv1.ListIndustriesRequest) (*orgv1.ListIndustriesResponse, error) {
	items, err := s.Catalog.Industries(ctx)
	if err != nil {
		return nil, s.internal("ListIndustries", err)
	}
	out := &orgv1.ListIndustriesResponse{}
	for _, it := range items {
		out.Industries = append(out.Industries, &orgv1.Industry{Id: it.ID, Slug: it.Slug, Name: it.Name})
	}
	return out, nil
}

// ValidateIndustry reports whether the slug is an active industry.
func (s *RPC) ValidateIndustry(ctx context.Context, req *orgv1.ValidateIndustryRequest) (*orgv1.ValidateIndustryResponse, error) {
	ok, err := s.Catalog.IndustryExists(ctx, req.GetSlug())
	if err != nil {
		return nil, s.internal("ValidateIndustry", err)
	}
	return &orgv1.ValidateIndustryResponse{Valid: ok}, nil
}

// GetKybStatus returns the verification status of the company in the call context.
func (s *RPC) GetKybStatus(ctx context.Context, _ *orgv1.GetKybStatusRequest) (*orgv1.GetKybStatusResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	cc, _ := rpc.CallContextFrom(ctx)
	if cc.GetCompanyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "call context has no company id (x-openhr-company-id)")
	}
	c, err := s.Store.Company(ctx, reg, cc.GetCompanyId())
	if err != nil {
		return nil, s.internal("GetKybStatus", err)
	}
	out := &orgv1.GetKybStatusResponse{Status: kybStatus(c.KYBStatus)}
	if s.KYB != nil {
		if rec, rerr := s.KYB.Status(ctx, reg, c.ID); rerr == nil && rec != nil {
			out.FailureReason = string(rec.FailureReason)
		}
	}
	return out, nil
}

// SubmitKyb is not served over gRPC: the verification needs the legal name and address, which the request does not
// carry. The HTTP flow (POST /organization/verification) is the entry point.
func (s *RPC) SubmitKyb(context.Context, *orgv1.SubmitKybRequest) (*orgv1.SubmitKybResponse, error) {
	return nil, status.Error(codes.Unimplemented, "SubmitKyb: the request lacks legal name and address; use POST /organization/verification")
}

// ApplyKybDecision records a reviewer's decision on a company's KYB case (identity calls it after closing the
// queue item). The company's region comes from the request.
func (s *RPC) ApplyKybDecision(ctx context.Context, req *orgv1.ApplyKybDecisionRequest) (*orgv1.ApplyKybDecisionResponse, error) {
	reg := region.Region(req.GetRegion())
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return nil, status.Errorf(codes.InvalidArgument, "region %q is not a company region", req.GetRegion())
	}
	if req.GetCompanyId() == "" || req.GetReviewerAdminId() == "" {
		return nil, status.Error(codes.InvalidArgument, "company_id and reviewer_admin_id are required")
	}
	if s.KYB == nil {
		return nil, status.Error(codes.Unavailable, "KYB review is not configured")
	}
	reason := kyb.FailureReason(req.GetReason())
	if !req.GetApprove() && !reason.Valid() {
		return nil, status.Error(codes.InvalidArgument, "a rejection needs a valid failure reason")
	}
	rec, err := s.KYB.Review(ctx, reg, req.GetCompanyId(), req.GetReviewerAdminId(), req.GetApprove(), reason)
	if err != nil {
		if rec == nil {
			return nil, s.internal("ApplyKybDecision", err)
		}
		s.log().Warn("kyb decision stored but closing the queue failed", "company", req.GetCompanyId(), "error", err)
	}
	return &orgv1.ApplyKybDecisionResponse{Status: kybStatus(string(rec.Status))}, nil
}

// DeleteCompany marks a company deleted and emits org.company.deleted.v1 (see orgstore.DeleteCompany).
func (s *RPC) DeleteCompany(ctx context.Context, req *orgv1.DeleteCompanyRequest) (*orgv1.DeleteCompanyResponse, error) {
	reg := region.Region(req.GetRegion())
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return nil, status.Errorf(codes.InvalidArgument, "region %q is not a company region", req.GetRegion())
	}
	if req.GetCompanyId() == "" || req.GetAdminId() == "" {
		return nil, status.Error(codes.InvalidArgument, "company_id and admin_id are required")
	}
	at, already, err := s.Store.DeleteCompany(ctx, reg, req.GetCompanyId(), req.GetReason())
	if err != nil {
		return nil, s.internal("DeleteCompany", err)
	}
	s.log().Info("company deleted", "company", req.GetCompanyId(), "region", req.GetRegion(), "admin", req.GetAdminId(), "already", already)
	return &orgv1.DeleteCompanyResponse{DeletedAt: timestamppb.New(at), AlreadyDeleted: already}, nil
}

// ReactivateCompany undoes DeleteCompany and emits org.company.reactivated.v1 (see orgstore.ReactivateCompany).
func (s *RPC) ReactivateCompany(ctx context.Context, req *orgv1.ReactivateCompanyRequest) (*orgv1.ReactivateCompanyResponse, error) {
	reg := region.Region(req.GetRegion())
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return nil, status.Errorf(codes.InvalidArgument, "region %q is not a company region", req.GetRegion())
	}
	if req.GetCompanyId() == "" || req.GetAdminId() == "" {
		return nil, status.Error(codes.InvalidArgument, "company_id and admin_id are required")
	}
	at, already, err := s.Store.ReactivateCompany(ctx, reg, req.GetCompanyId(), req.GetReason())
	if err != nil {
		return nil, s.internal("ReactivateCompany", err)
	}
	s.log().Info("company reactivated", "company", req.GetCompanyId(), "region", req.GetRegion(), "admin", req.GetAdminId(), "already", already)
	return &orgv1.ReactivateCompanyResponse{ReactivatedAt: timestamppb.New(at), AlreadyActive: already}, nil
}

// CompleteRegionOnboarding runs the region-migration saga for the user: identity copies the user, org copies the
// organization draft and progress, identity commits, org releases the holding rows and emits org.region.moved.v1.
// Every step is persisted (accounts.region_move_sagas), so calling it again after any failure resumes or
// re-attempts. Needs REGION_MOVE=saga. It issues no tokens: the caller rotates its session.
func (s *RPC) CompleteRegionOnboarding(ctx context.Context, req *orgv1.CompleteRegionOnboardingRequest) (*orgv1.CompleteRegionOnboardingResponse, error) {
	if s.Mover == nil || s.Countries == nil {
		return nil, status.Error(codes.Unimplemented, "the region-migration saga is off: set REGION_MOVE=saga")
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "user_id is not a uuid")
	}
	countryID, err := uuid.Parse(req.GetCountryId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "country_id is not a uuid")
	}
	target, err := s.Countries.CountryRegion(ctx, countryID)
	if err != nil {
		return nil, s.internal("CompleteRegionOnboarding", err)
	}
	if !region.Valid(target) || target == region.RegionGlobal {
		return nil, status.Error(codes.InvalidArgument, "unknown or inactive country")
	}
	reg, err := s.Mover.Move(ctx, userID, target)
	switch {
	case errors.Is(err, regionsaga.ErrConflict):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, regionsaga.ErrBusy):
		return nil, status.Error(codes.Aborted, err.Error())
	case errors.Is(err, regionsaga.ErrCompensated):
		return nil, status.Error(codes.Aborted, "the move failed and was rolled back; the account is unchanged, retry")
	case err != nil:
		return nil, s.internal("CompleteRegionOnboarding", err)
	}
	return &orgv1.CompleteRegionOnboardingResponse{Region: string(reg)}, nil
}

// GetCompanyByOwner returns the company a user owns (admin user detail page).
func (s *RPC) GetCompanyByOwner(ctx context.Context, req *orgv1.GetCompanyByOwnerRequest) (*orgv1.GetCompanyByOwnerResponse, error) {
	reg, err := callRegion(ctx)
	if err != nil {
		return nil, err
	}
	c, err := s.Store.CompanyByOwner(ctx, reg, req.GetOwnerUserId())
	if err != nil {
		return nil, s.internal("GetCompanyByOwner", err)
	}
	return &orgv1.GetCompanyByOwnerResponse{Company: companyMsg(c)}, nil
}

var (
	_ orgv1.OrgServiceServer      = (*RPC)(nil)
	_ orgv1.AdminOrgServiceServer = (*RPC)(nil)
)

// orgAllow restricts the methods only some peers call (enforced when GRPC_AUTH is token or mtls). "server" is the
// monolith, which makes the same calls while a service is not extracted.
var orgAllow = rpc.AllowList{
	orgv1.OrgService_ResolveMembership_FullMethodName:      {"hiring", "applications", "identity", "server"},
	orgv1.OrgService_GetOnboardingProgress_FullMethodName:  {"identity", "server"},
	orgv1.OrgService_GetPublishReadiness_FullMethodName:    {"hiring", "applications", "identity", "server"},
	orgv1.AdminOrgService_ApplyKybDecision_FullMethodName:  {"identity", "server"},
	orgv1.AdminOrgService_GetCompanyByOwner_FullMethodName: {"identity", "server"},
	orgv1.AdminOrgService_DeleteCompany_FullMethodName:     {"identity", "server"},
	orgv1.AdminOrgService_ReactivateCompany_FullMethodName: {"identity", "server"},
}
