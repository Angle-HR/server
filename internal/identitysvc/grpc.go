package identitysvc

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/identityhttp"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/internal/userdir"
	"github.com/Angle-HR/server/internal/verifyqueue"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rpc"
)

// RPC implements openhr.identity.v1.IdentityService and AdminIdentityService.
//
// The gRPC listener has no transport authentication (like the other services): keep GRPC_ADDR on the internal
// network. AdminIdentityService trusts reviewer_admin_id and the caller's call context; admin-bff is expected to
// have authenticated the operator and checked HasAdminPermission first.
//
// Implemented: ValidateAccessToken, ValidateAdminToken (the "token" is the session cookie value), GetUser,
// BatchGetUsers, LookupRegion, ListUsers, SetUserActive, InviteStaff, ListAdminRoles, HasAdminPermission,
// ListVerificationQueue, ReviewVerification, EnqueueVerification.
//
// Unimplemented on purpose (answered with codes.Unimplemented and a message that says why): PrepareRegionMove,
// CommitRegionMove (the saga still runs inside the onboarding handlers and the contract returns a JWT TokenPair,
// which no longer exists: sessions rotate instead), CreateOrgInvite, AcceptOrgInvite (the contract has no company
// context or session result; invites are served over HTTP).
type RPC struct {
	identityv1.UnimplementedIdentityServiceServer
	admin *adminRPC

	Sessions      *session.Manager // product sessions
	AdminSessions *session.Manager
	// RequireAdminMFA (ADMIN_TOTP_REQUIRED): an admin session that has not passed a second factor does not validate.
	RequireAdminMFA bool
	Users           *userdir.Directory
	AdminHandler    *identityhttp.AdminHandler
	Admins          *admin.Store
}

type adminRPC struct {
	identityv1.UnimplementedAdminIdentityServiceServer
	*RPC
}

// NewGRPCServer returns a gRPC server with both services registered and the CallContext interceptor installed.
func NewGRPCServer(r *RPC, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(append(rpc.ServerOptions("identity", identityAllow), grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{}))), opts...)
	s := grpc.NewServer(opts...)
	identityv1.RegisterIdentityServiceServer(s, r)
	identityv1.RegisterAdminIdentityServiceServer(s, &adminRPC{RPC: r})
	return s
}

func grpcError(err error) error {
	var ae *apperror.AppError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, userdir.ErrNotFound), errors.Is(err, verifyqueue.ErrNoPendingItem):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, apperror.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	case errors.Is(err, session.ErrInvalid):
		return status.Error(codes.Unauthenticated, "session invalid")
	case errors.Is(err, session.ErrUnavailable):
		return status.Error(codes.Unavailable, "session store unavailable")
	case errors.As(err, &ae):
		switch ae.Code {
		case apperror.CodeNotFound:
			return status.Error(codes.NotFound, ae.Message)
		case apperror.CodeValidationError:
			return status.Error(codes.InvalidArgument, ae.Message)
		case apperror.CodeConflict:
			return status.Error(codes.FailedPrecondition, ae.Message)
		case apperror.CodeServiceUnavailable:
			return status.Error(codes.Unavailable, ae.Message)
		}
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, "internal error")
}

func userProto(u userdir.User) *identityv1.User {
	return &identityv1.User{
		Id: u.ID, Email: u.Email, FirstName: u.FirstName, LastName: u.LastName, LegalFullName: u.LegalFullName,
		AccountType: u.AccountType, Region: u.Region, EmailVerified: u.EmailVerified, Active: u.Active,
		CreatedAt: timestamppb.New(u.CreatedAt),
	}
}

func parseID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "%s is not a uuid", field)
	}
	return id, nil
}

// ValidateAccessToken validates a product session cookie value. Only fully authenticated sessions validate (not
// the MFA pre-auth stage). Validating slides the idle window, as a request through the monolith does.
func (s *RPC) ValidateAccessToken(ctx context.Context, req *identityv1.ValidateAccessTokenRequest) (*identityv1.ValidateAccessTokenResponse, error) {
	ident, err := s.Sessions.Validator().Validate(ctx, req.GetToken())
	if err != nil {
		return nil, grpcError(err)
	}
	if ident.Stage != session.StageFull {
		return nil, status.Error(codes.Unauthenticated, "session invalid")
	}
	return &identityv1.ValidateAccessTokenResponse{
		UserId: ident.UserID.String(), Region: ident.Region,
		// A full session only exists after the email was verified (login refuses unverified accounts).
		EmailVerified: true,
		ExpiresAt:     timestamppb.New(ident.CreatedAt.Add(s.Sessions.Sessions.AbsoluteTTL())),
	}, nil
}

// ValidateAdminToken validates an admin session cookie value and returns the operator's permissions. A deactivated
// operator does not validate.
func (s *RPC) ValidateAdminToken(ctx context.Context, req *identityv1.ValidateAdminTokenRequest) (*identityv1.ValidateAdminTokenResponse, error) {
	ident, err := s.AdminSessions.Validator().Validate(ctx, req.GetToken())
	if err != nil {
		return nil, grpcError(err)
	}
	if ident.Stage != session.StageFull || (s.RequireAdminMFA && !ident.MFA) {
		return nil, status.Error(codes.Unauthenticated, "session invalid")
	}
	p, err := s.Admins.GetAdminPrincipal(ctx, ident.UserID)
	if err != nil || !p.IsActive {
		return nil, status.Error(codes.Unauthenticated, "session invalid")
	}
	perms, err := s.Admins.ListAdminPermissions(ctx, ident.UserID)
	if err != nil {
		return nil, grpcError(err)
	}
	return &identityv1.ValidateAdminTokenResponse{
		AdminUserId: ident.UserID.String(), Permissions: perms,
		ExpiresAt: timestamppb.New(ident.CreatedAt.Add(s.AdminSessions.Sessions.AbsoluteTTL())),
	}, nil
}

// GetUser returns one user (by user id or registry id).
func (s *RPC) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	id, err := parseID(req.GetUserId(), "user_id")
	if err != nil {
		return nil, err
	}
	u, err := s.Users.Get(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	return &identityv1.GetUserResponse{User: userProto(u)}, nil
}

// maxBatch bounds BatchGetUsers.
const maxBatch = 500

// BatchGetUsers resolves display data for many users. Unknown ids are skipped.
func (s *RPC) BatchGetUsers(ctx context.Context, req *identityv1.BatchGetUsersRequest) (*identityv1.BatchGetUsersResponse, error) {
	if len(req.GetUserIds()) > maxBatch {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d user ids per call", maxBatch)
	}
	ids := make([]uuid.UUID, 0, len(req.GetUserIds()))
	for _, raw := range req.GetUserIds() {
		id, err := parseID(raw, "user_ids")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	users, err := s.Users.Batch(ctx, ids, req.GetExcludeInactive())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &identityv1.BatchGetUsersResponse{}
	for _, u := range users {
		out.Users = append(out.Users, userProto(u))
	}
	return out, nil
}

// LookupRegion answers which region holds the user.
func (s *RPC) LookupRegion(ctx context.Context, req *identityv1.LookupRegionRequest) (*identityv1.LookupRegionResponse, error) {
	var email string
	var id uuid.UUID
	switch k := req.GetKey().(type) {
	case *identityv1.LookupRegionRequest_Email:
		email = strings.TrimSpace(k.Email)
	case *identityv1.LookupRegionRequest_UserId:
		var err error
		if id, err = parseID(k.UserId, "user_id"); err != nil {
			return nil, err
		}
	}
	if email == "" && id == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "email or user_id is required")
	}
	uid, reg, found, err := s.Users.LookupRegion(ctx, email, id)
	if err != nil {
		return nil, grpcError(err)
	}
	if !found {
		return &identityv1.LookupRegionResponse{}, nil
	}
	return &identityv1.LookupRegionResponse{UserId: uid.String(), Region: reg, Found: true}, nil
}

// PrepareRegionMove is not backed: see the RPC type comment.
func (s *RPC) PrepareRegionMove(context.Context, *identityv1.PrepareRegionMoveRequest) (*identityv1.PrepareRegionMoveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "the region move still runs inside the onboarding handlers (org); the saga is not split yet")
}

// CommitRegionMove is not backed: see the RPC type comment.
func (s *RPC) CommitRegionMove(context.Context, *identityv1.CommitRegionMoveRequest) (*identityv1.CommitRegionMoveResponse, error) {
	return nil, status.Error(codes.Unimplemented, "the region move still runs inside the onboarding handlers (org); sessions rotate instead of a TokenPair")
}

// CreateOrgInvite is not backed: invites are created over HTTP (POST /organizations/invites).
func (s *RPC) CreateOrgInvite(context.Context, *identityv1.CreateOrgInviteRequest) (*identityv1.CreateOrgInviteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "org invites are created over HTTP; the contract has no company context")
}

// AcceptOrgInvite is not backed: acceptance creates a browser session, so it is served over HTTP.
func (s *RPC) AcceptOrgInvite(context.Context, *identityv1.AcceptOrgInviteRequest) (*identityv1.AcceptOrgInviteResponse, error) {
	return nil, status.Error(codes.Unimplemented, "invite acceptance starts a session and is served over HTTP (POST /auth/accept-invite)")
}

const (
	defaultPage = 50
	maxPage     = 100
)

// ListUsers pages the registry, newest first. page_token is the offset of the next page.
func (a *adminRPC) ListUsers(ctx context.Context, req *identityv1.ListUsersRequest) (*identityv1.ListUsersResponse, error) {
	size := int(req.GetPageSize())
	if size <= 0 {
		size = defaultPage
	}
	size = min(size, maxPage)
	offset := 0
	if t := req.GetPageToken(); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			return nil, status.Error(codes.InvalidArgument, "page_token is not valid")
		}
		offset = n
	}
	if reg := req.GetRegion(); reg != "" {
		if _, err := region.ParseRegion(reg); err != nil {
			return nil, status.Error(codes.InvalidArgument, "unknown region")
		}
	}
	rows, err := a.Users.List(ctx, strings.TrimSpace(req.GetQuery()), req.GetRegion(), size+1, offset)
	if err != nil {
		return nil, grpcError(err)
	}
	next := ""
	if len(rows) > size {
		rows = rows[:size]
		next = strconv.Itoa(offset + size)
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.UserID
	}
	details, err := a.Users.Batch(ctx, ids, false)
	if err != nil {
		return nil, grpcError(err)
	}
	byID := map[string]userdir.User{}
	for _, u := range details {
		byID[u.ID] = u
	}
	out := &identityv1.ListUsersResponse{NextPageToken: next}
	for _, r := range rows {
		u, ok := byID[r.UserID.String()]
		if !ok { // registry row without a regional row yet: show what the registry knows
			u = userdir.User{ID: r.UserID.String(), Email: r.Email, Region: r.Region, CreatedAt: r.CreatedAt}
		}
		out.Users = append(out.Users, userProto(u))
	}
	return out, nil
}

// SetUserActive deactivates or reactivates an account. Deactivating also ends the user's sessions.
func (a *adminRPC) SetUserActive(ctx context.Context, req *identityv1.SetUserActiveRequest) (*identityv1.SetUserActiveResponse, error) {
	id, err := parseID(req.GetUserId(), "user_id")
	if err != nil {
		return nil, err
	}
	cc, _ := rpc.CallContextFrom(ctx)
	u, err := a.Users.SetActive(ctx, id, req.GetActive(), cc.GetUserId())
	if err != nil {
		return nil, grpcError(err)
	}
	if !req.GetActive() {
		uid, perr := uuid.Parse(u.ID)
		if perr == nil {
			if rerr := a.Sessions.RevokeAll(ctx, uid); rerr != nil {
				return nil, grpcError(rerr)
			}
		}
	}
	return &identityv1.SetUserActiveResponse{User: userProto(u)}, nil
}

// InviteStaff invites a platform staff member with one role (role_id is the role's uuid or slug). The acting admin
// comes from the call context user id.
func (a *adminRPC) InviteStaff(ctx context.Context, req *identityv1.InviteStaffRequest) (*identityv1.InviteStaffResponse, error) {
	email := strings.ToLower(strings.TrimSpace(req.GetEmail()))
	if email == "" || !strings.Contains(email, "@") || len(email) > 254 {
		return nil, status.Error(codes.InvalidArgument, "email is not valid")
	}
	roles, err := a.Admins.ListRolesWithPermissions(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	slug := ""
	for _, r := range roles {
		if r.ID.String() == req.GetRoleId() || r.Slug == req.GetRoleId() {
			slug = r.Slug
		}
	}
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "unknown role_id")
	}
	cc, _ := rpc.CallContextFrom(ctx)
	actor, _ := uuid.Parse(cc.GetUserId())
	res, err := a.AdminHandler.InviteStaff(ctx, email, "", []string{slug}, actor)
	if err != nil {
		return nil, grpcError(err)
	}
	return &identityv1.InviteStaffResponse{InviteId: res.Staff.ID.String()}, nil
}

// ListAdminRoles lists admin roles with their permissions.
func (a *adminRPC) ListAdminRoles(ctx context.Context, _ *identityv1.ListAdminRolesRequest) (*identityv1.ListAdminRolesResponse, error) {
	roles, err := a.Admins.ListRolesWithPermissions(ctx)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &identityv1.ListAdminRolesResponse{}
	for _, r := range roles {
		out.Roles = append(out.Roles, &identityv1.AdminRole{Id: r.ID.String(), Name: r.Name, Permissions: r.Permissions})
	}
	return out, nil
}

// HasAdminPermission answers an admin RBAC check. An inactive operator has no permissions.
func (a *adminRPC) HasAdminPermission(ctx context.Context, req *identityv1.HasAdminPermissionRequest) (*identityv1.HasAdminPermissionResponse, error) {
	id, err := parseID(req.GetAdminUserId(), "admin_user_id")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetPermission()) == "" {
		return nil, status.Error(codes.InvalidArgument, "permission is required")
	}
	p, err := a.Admins.GetAdminPrincipal(ctx, id)
	if err != nil || !p.IsActive {
		return &identityv1.HasAdminPermissionResponse{Allowed: false}, nil //nolint:nilerr // unknown or inactive: no permission
	}
	perms, err := a.Admins.ListAdminPermissions(ctx, id)
	if err != nil {
		return nil, grpcError(err)
	}
	for _, p := range perms {
		if p == req.GetPermission() {
			return &identityv1.HasAdminPermissionResponse{Allowed: true}, nil
		}
	}
	return &identityv1.HasAdminPermissionResponse{}, nil
}

// ListVerificationQueue lists the open KYB review items, oldest first. page_token is the offset of the next page.
func (a *adminRPC) ListVerificationQueue(ctx context.Context, req *identityv1.ListVerificationQueueRequest) (*identityv1.ListVerificationQueueResponse, error) {
	size := int(req.GetPageSize())
	if size <= 0 {
		size = defaultPage
	}
	size = min(size, maxPage)
	offset := 0
	if t := req.GetPageToken(); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 {
			return nil, status.Error(codes.InvalidArgument, "page_token is not valid")
		}
		offset = n
	}
	items, err := a.AdminHandler.Queue.List(ctx, req.GetRegion(), size+1, offset)
	if err != nil {
		return nil, grpcError(err)
	}
	next := ""
	if len(items) > size {
		items = items[:size]
		next = strconv.Itoa(offset + size)
	}
	out := &identityv1.ListVerificationQueueResponse{NextPageToken: next}
	for _, it := range items {
		out.Items = append(out.Items, &identityv1.VerificationQueueItem{
			CompanyId: it.OrganizationID, Region: it.Region, CountryCode: it.CountryCode,
			SubmittedAt: timestamppb.New(it.SubmittedAt), CaseType: it.CaseType,
		})
	}
	return out, nil
}

// ReviewVerification records the decision on the company's case through org (ORG_APPLY) and closes the queue item.
func (a *adminRPC) ReviewVerification(ctx context.Context, req *identityv1.ReviewVerificationRequest) (*identityv1.ReviewVerificationResponse, error) {
	if _, err := parseID(req.GetCompanyId(), "company_id"); err != nil {
		return nil, err
	}
	reviewer, err := parseID(req.GetReviewerAdminId(), "reviewer_admin_id")
	if err != nil {
		return nil, err
	}
	if a.AdminHandler.Org == nil {
		return nil, status.Error(codes.Unavailable, "no org adapter configured")
	}
	reg, err := region.ParseRegion(req.GetRegion())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "unknown region")
	}
	item, err := a.AdminHandler.Queue.Pending(ctx, req.GetCompanyId())
	if err != nil {
		return nil, grpcError(err)
	}
	if item.Region != string(reg) {
		return nil, status.Error(codes.InvalidArgument, "region does not match the queued item")
	}
	out, err := a.AdminHandler.ReviewCase(ctx, req.GetCompanyId(), reg, reviewer.String(), req.GetApprove(), req.GetReason())
	if out.Status != "" {
		decision := "reject"
		if req.GetApprove() {
			decision = "approve"
		}
		a.AdminHandler.AuditAs(ctx, reviewer, "verification.review."+decision, "organization_verification",
			req.GetCompanyId(), map[string]any{"region": string(reg), "reason": req.GetReason(), "status": out.Status})
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &identityv1.ReviewVerificationResponse{Status: map[string]string{"verified": "approved", "failed": "rejected"}[out.Status]}, nil
}

// EnqueueVerification opens a review item (called by org). Re-queueing an open case is a no-op.
func (a *adminRPC) EnqueueVerification(ctx context.Context, req *identityv1.EnqueueVerificationRequest) (*identityv1.EnqueueVerificationResponse, error) {
	if _, err := parseID(req.GetCompanyId(), "company_id"); err != nil {
		return nil, err
	}
	if _, err := region.ParseRegion(req.GetRegion()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "unknown region")
	}
	q := &verifyqueue.Queue{DB: a.AdminHandler.Queue.DB, Region: req.GetRegion()}
	switch req.GetCaseType() {
	case "", verifyqueue.CaseBusiness, verifyqueue.CaseIndividual:
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown case_type")
	}
	if err := q.EnqueueCase(ctx, req.GetCompanyId(), req.GetCountryCode(), req.GetCaseType()); err != nil {
		return nil, grpcError(err)
	}
	return &identityv1.EnqueueVerificationResponse{}, nil
}

// identityAllow: only org opens KYB review items (the monolith when org is not extracted).
var identityAllow = rpc.AllowList{
	identityv1.AdminIdentityService_EnqueueVerification_FullMethodName: {"org", "server"},
}
