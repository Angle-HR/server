package applicationsvc

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
)

// RPC implements openhr.applications.v1.ApplicationsService over the same application services the HTTP routes
// use. The caller (company, user, region, permissions) is read from the pkg/rpc CallContext metadata, so the
// server must be built with rpc.ServerUnaryInterceptor.
//
// SubmitApplication, CreateUploadUrl and ConfirmUpload are the public (anonymous) calls: they need no user in the
// call context, see grpc_public.go.
type RPC struct {
	applicationsv1.UnimplementedApplicationsServiceServer
	// Applications returns a region's service.
	Applications apphttp.RegionalApplications
	// Lookup resolves an application id to its job and reads applicant counters.
	Lookup Lookup
	// Apply takes public applications; Uploads issues and verifies upload targets. Nil answers Unavailable
	// (uploads are nil without a scanner, as over HTTP).
	Apply   Applier
	Uploads Uploader
}

// NewGRPCServer returns a gRPC server with the ApplicationsService registered and the CallContext interceptor
// installed.
func NewGRPCServer(r *RPC, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(append(rpc.ServerOptions("applications", applicationsAllow), grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{}))), opts...)
	s := grpc.NewServer(opts...)
	applicationsv1.RegisterApplicationsServiceServer(s, r)
	return s
}

// callContext turns the CallContext of the request into the company caller and its region.
func callContext(ctx context.Context) (applications.Caller, region.Region, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	if cc.GetUserId() == "" {
		return applications.Caller{}, "", status.Error(codes.Unauthenticated, "call context has no user id (x-openhr-user-id)")
	}
	reg := region.Region(cc.GetRegion())
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return applications.Caller{}, "", status.Errorf(codes.InvalidArgument, "call context has no usable region (x-openhr-region=%q)", cc.GetRegion())
	}
	if cc.GetCompanyId() == "" {
		return applications.Caller{}, "", status.Error(codes.InvalidArgument, "call context has no company id (x-openhr-company-id)")
	}
	perms := make([]rbac.Permission, 0, len(cc.GetPermissions()))
	for _, p := range cc.GetPermissions() {
		perms = append(perms, rbac.Permission(p))
	}
	return applications.Caller{
		UserID: cc.GetUserId(), OrgID: cc.GetCompanyId(), Region: string(reg), Perms: rbac.NewSet(perms...),
	}, reg, nil
}

func (s *RPC) service(reg region.Region) (*applications.Service, error) {
	if s.Applications == nil {
		return nil, status.Error(codes.Unavailable, "applications are not available")
	}
	svc, err := s.Applications(reg)
	if err != nil {
		return nil, grpcError(err)
	}
	return svc, nil
}

// ListApplications lists a job's applications, newest first. page_token is the opaque cursor of the previous page.
func (s *RPC) ListApplications(ctx context.Context, req *applicationsv1.ListApplicationsRequest) (*applicationsv1.ListApplicationsResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.service(reg)
	if err != nil {
		return nil, err
	}
	q := applications.ListQuery{Cursor: req.GetPageToken(), Limit: int(req.GetPageSize())}
	for _, st := range req.GetStatuses() {
		slug, ok := statusSlug(st)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown status %v", st)
		}
		q.Statuses = append(q.Statuses, slug)
	}
	page, err := svc.List(ctx, c, req.GetJobId(), q)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &applicationsv1.ListApplicationsResponse{NextPageToken: page.NextCursor}
	for _, it := range page.Items {
		out.Applications = append(out.Applications, summaryProto(it, req.GetJobId(), c.OrgID))
	}
	return out, nil
}

// GetApplication opens one application (this is recorded in the job's audit log, as opening it over HTTP is). The
// job id is looked up from the application id.
func (s *RPC) GetApplication(ctx context.Context, req *applicationsv1.GetApplicationRequest) (*applicationsv1.GetApplicationResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.service(reg)
	if err != nil {
		return nil, err
	}
	jobID, err := s.jobOf(ctx, reg, c, req.GetApplicationId())
	if err != nil {
		return nil, err
	}
	d, err := svc.Get(ctx, c, jobID, req.GetApplicationId())
	if err != nil {
		return nil, grpcError(err)
	}
	answers, err := json.Marshal(d.Answers)
	if err != nil {
		return nil, status.Error(codes.Internal, "encode answers")
	}
	return &applicationsv1.GetApplicationResponse{
		Application: summaryProto(d.Summary, jobID, c.OrgID), AnswersJson: string(answers),
	}, nil
}

// ChangeStatus makes a plain status change (a flagged application is decided through its review, which the
// contract has no call for yet, so it answers FailedPrecondition).
func (s *RPC) ChangeStatus(ctx context.Context, req *applicationsv1.ChangeStatusRequest) (*applicationsv1.ChangeStatusResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	slug, ok := statusSlug(req.GetStatus())
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown status %v", req.GetStatus())
	}
	svc, err := s.service(reg)
	if err != nil {
		return nil, err
	}
	jobID, err := s.jobOf(ctx, reg, c, req.GetApplicationId())
	if err != nil {
		return nil, err
	}
	res, err := svc.SetStatus(ctx, c, jobID, req.GetApplicationId(), applications.StatusInput{Status: slug, Note: req.GetNote()})
	if err != nil {
		return nil, grpcError(err)
	}
	return &applicationsv1.ChangeStatusResponse{Application: &applicationsv1.Application{
		Id: req.GetApplicationId(), JobId: jobID, CompanyId: c.OrgID, Status: statusEnum(res.Status),
	}}, nil
}

func (s *RPC) jobOf(ctx context.Context, reg region.Region, c applications.Caller, appID string) (string, error) {
	if appID == "" {
		return "", status.Error(codes.InvalidArgument, "application_id is required")
	}
	if s.Lookup == nil {
		return "", status.Error(codes.Unavailable, "application lookup is not available")
	}
	jobID, err := s.Lookup.JobOfApplication(ctx, string(reg), c.OrgID, appID)
	if err != nil {
		return "", grpcError(err)
	}
	return jobID, nil
}

// GetSettings returns the company's candidate settings.
func (s *RPC) GetSettings(ctx context.Context, _ *applicationsv1.GetSettingsRequest) (*applicationsv1.GetSettingsResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.service(reg)
	if err != nil {
		return nil, err
	}
	st, err := svc.Settings(ctx, c)
	if err != nil {
		return nil, grpcError(err)
	}
	return &applicationsv1.GetSettingsResponse{ReapplyBlockMonths: int32(st.ReapplyBlockMonths), SendStatusEmails: st.SendStatusEmails}, nil
}

// UpdateSettings changes the company's candidate settings; a field left out stays as it is.
func (s *RPC) UpdateSettings(ctx context.Context, req *applicationsv1.UpdateSettingsRequest) (*applicationsv1.UpdateSettingsResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := s.service(reg)
	if err != nil {
		return nil, err
	}
	in := applications.SettingsInput{}
	if req.ReapplyBlockMonths != nil {
		v := int(req.GetReapplyBlockMonths())
		in.ReapplyBlockMonths = &v
	}
	if req.SendStatusEmails != nil {
		v := req.GetSendStatusEmails()
		in.SendStatusEmails = &v
	}
	st, err := svc.SaveSettings(ctx, c, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &applicationsv1.UpdateSettingsResponse{ReapplyBlockMonths: int32(st.ReapplyBlockMonths), SendStatusEmails: st.SendStatusEmails}, nil
}

// GetApplicantCounts returns the per-job counters. Jobs with no applications are returned with zeros.
func (s *RPC) GetApplicantCounts(ctx context.Context, req *applicationsv1.GetApplicantCountsRequest) (*applicationsv1.GetApplicantCountsResponse, error) {
	c, reg, err := callContext(ctx)
	if err != nil {
		return nil, err
	}
	if s.Lookup == nil {
		return nil, status.Error(codes.Unavailable, "application lookup is not available")
	}
	rows, err := s.Lookup.ApplicantCounts(ctx, string(reg), c.OrgID, req.GetJobIds())
	if err != nil {
		return nil, grpcError(err)
	}
	byJob := map[string]*applicationsv1.ApplicantCount{}
	for _, r := range rows {
		byJob[r.JobID] = &applicationsv1.ApplicantCount{JobId: r.JobID, Total: int32(r.Total), NewCount: int32(r.New)}
	}
	out := &applicationsv1.GetApplicantCountsResponse{}
	for _, id := range req.GetJobIds() {
		if c, ok := byJob[id]; ok {
			out.Counts = append(out.Counts, c)
		} else {
			out.Counts = append(out.Counts, &applicationsv1.ApplicantCount{JobId: id})
		}
	}
	return out, nil
}

func summaryProto(s applications.Summary, jobID, companyID string) *applicationsv1.Application {
	return &applicationsv1.Application{
		Id: s.ID, JobId: jobID, CompanyId: companyID, CandidateName: s.Name, CandidateEmail: s.Email,
		Status: statusEnum(s.Status), SubmittedAt: timestamppb.New(s.SubmittedAt),
	}
}

var statusToSlug = map[applicationsv1.ApplicationStatus]string{
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_RECEIVED:  applications.StatusReceived,
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_IN_REVIEW: applications.StatusInReview,
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_FLAGGED:   applications.StatusFlagged,
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_REJECTED:  applications.StatusRejected,
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_WITHDRAWN: applications.StatusWithdrawn,
	applicationsv1.ApplicationStatus_APPLICATION_STATUS_HIRED:     applications.StatusHired,
}

func statusSlug(s applicationsv1.ApplicationStatus) (string, bool) {
	v, ok := statusToSlug[s]
	return v, ok
}

func statusEnum(slug string) applicationsv1.ApplicationStatus {
	for e, v := range statusToSlug {
		if v == slug {
			return e
		}
	}
	return applicationsv1.ApplicationStatus_APPLICATION_STATUS_UNSPECIFIED
}

// grpcError maps a service error to a gRPC status. Anything unexpected is Internal with no detail, so storage
// errors never reach the caller.
func grpcError(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	switch {
	case errors.Is(err, applications.ErrStale):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, applications.ErrInvalidTransition), errors.Is(err, applications.ErrReviewRequired),
		errors.Is(err, applications.ErrNoReview), errors.Is(err, applications.ErrNotOpened):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, apperror.ErrNotFound):
		return status.Error(codes.NotFound, "not found")
	}
	var ae *apperror.AppError
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperror.CodeNotFound:
			return status.Error(codes.NotFound, ae.Message)
		case apperror.CodeForbidden:
			return status.Error(codes.PermissionDenied, ae.Message)
		case apperror.CodeValidationError, apperror.CodeInvalidReference:
			return status.Error(codes.InvalidArgument, ae.Message)
		case apperror.CodeConflict:
			return status.Error(codes.FailedPrecondition, ae.Message)
		case apperror.CodeUnauthorized:
			return status.Error(codes.Unauthenticated, ae.Message)
		case apperror.CodeServiceUnavailable:
			return status.Error(codes.Unavailable, ae.Message)
		case apperror.CodeTooManyRequests:
			return status.Error(codes.ResourceExhausted, ae.Message)
		}
	}
	return status.Error(codes.Internal, "internal error")
}

// applicationsAllow: applicant counters are read by hiring (and the monolith while it serves hiring).
var applicationsAllow = rpc.AllowList{
	applicationsv1.ApplicationsService_GetApplicantCounts_FullMethodName: {"hiring", "server"},
}
