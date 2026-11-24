package hiringsvc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	hiringv1 "github.com/Angle-HR/server/gen/openhr/hiring/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/rpc"
)

// RPC implements openhr.hiring.v1.HiringService over the same draft and public services the HTTP routes use.
// The caller (company, user, region, permissions) is read from the pkg/rpc CallContext metadata, so the server
// must be built with rpc.ServerUnaryInterceptor (NewGRPCServer does).
//
// Implemented: CreateDraft, GetJob, ListJobs, PublishJob, PauseJob, ResumeJob, CloseJob, GetPublicJob,
// ListOpenJobs, CheckJobAccess, GetJobsSnapshot. Nothing is left Unimplemented.
//
// Contract notes: PublishJob returns OK with published=false and the blocking gates when the checks fail, but an
// unreachable org service (publish fails closed) is codes.Unavailable so the caller retries. GetPublicJob keeps
// details_json and form_schema_json as JSON strings.
type RPC struct {
	hiringv1.UnimplementedHiringServiceServer
	// Service returns the draft service of a region.
	Service func(reg region.Region) (*draft.Service, error)
	// Public reads public jobs through the global registry (no CallContext needed).
	Public *public.Service
	// Store reads job team data for CheckJobAccess.
	Access AccessReader
}

// AccessReader answers who is on a job, for CheckJobAccess.
type AccessReader interface {
	// JobTeam returns the job's creator and the role of userID in its team ("" when not a member).
	JobTeam(ctx context.Context, reg region.Region, companyID, jobID, userID string) (createdBy, role string, err error)
}

// NewGRPCServer returns a gRPC server with the HiringService registered and the CallContext interceptor installed.
func NewGRPCServer(r *RPC, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(append(rpc.ServerOptions("hiring", nil), grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{}))), opts...)
	s := grpc.NewServer(opts...)
	hiringv1.RegisterHiringServiceServer(s, r)
	return s
}

// caller turns the CallContext into the draft caller and its region.
func caller(ctx context.Context) (draft.Caller, region.Region, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	if cc.GetUserId() == "" {
		return draft.Caller{}, "", status.Error(codes.Unauthenticated, "call context has no user id (x-openhr-user-id)")
	}
	reg := region.Region(cc.GetRegion())
	if reg == region.RegionGlobal || !region.Valid(reg) {
		return draft.Caller{}, "", status.Errorf(codes.InvalidArgument, "call context has no usable region (x-openhr-region=%q)", cc.GetRegion())
	}
	if cc.GetCompanyId() == "" {
		return draft.Caller{}, "", status.Error(codes.InvalidArgument, "call context has no company id (x-openhr-company-id)")
	}
	perms := make([]rbac.Permission, 0, len(cc.GetPermissions()))
	for _, p := range cc.GetPermissions() {
		perms = append(perms, rbac.Permission(p))
	}
	return draft.Caller{UserID: cc.GetUserId(), OrgID: cc.GetCompanyId(), Region: string(reg), Perms: rbac.NewSet(perms...)}, reg, nil
}

func (s *RPC) scope(ctx context.Context) (*draft.Service, draft.Caller, error) {
	c, reg, err := caller(ctx)
	if err != nil {
		return nil, c, err
	}
	if s.Service == nil {
		return nil, c, status.Error(codes.Unavailable, "hiring is not available")
	}
	svc, err := s.Service(reg)
	if err != nil {
		return nil, c, grpcError(err)
	}
	return svc, c, nil
}

// CreateDraft creates a draft job. details_json is a job patch (the PATCH /jobs/{id} body shape); template_id
// starts from a saved template.
func (s *RPC) CreateDraft(ctx context.Context, req *hiringv1.CreateDraftRequest) (*hiringv1.CreateDraftResponse, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	var in draft.CreateRequest
	if raw := strings.TrimSpace(req.GetDetailsJson()); raw != "" {
		if in.Patch, err = jobs.DecodePatch([]byte(raw)); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "details_json: %v", err)
		}
	}
	if t := req.GetTemplateId(); t != "" {
		in.TemplateID = &t
	}
	v, err := svc.Create(ctx, c, in)
	if err != nil {
		return nil, grpcError(err)
	}
	return &hiringv1.CreateDraftResponse{Job: jobProto(v, c.OrgID)}, nil
}

// GetJob reads one job the caller may see.
func (s *RPC) GetJob(ctx context.Context, req *hiringv1.GetJobRequest) (*hiringv1.GetJobResponse, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	v, err := svc.Get(ctx, c, req.GetJobId())
	if err != nil {
		return nil, grpcError(err)
	}
	return &hiringv1.GetJobResponse{Job: jobProto(v, c.OrgID)}, nil
}

// ListJobs lists the jobs the caller may see, newest updated first. page_token is the opaque cursor of the
// previous page.
func (s *RPC) ListJobs(ctx context.Context, req *hiringv1.ListJobsRequest) (*hiringv1.ListJobsResponse, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	q := draft.ListQuery{Cursor: req.GetPageToken(), Limit: int(req.GetPageSize())}
	for _, st := range req.GetStatuses() {
		slug, ok := statusSlug(st)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown status %v", st)
		}
		q.Statuses = append(q.Statuses, slug)
	}
	items, next, err := svc.List(ctx, c, q)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &hiringv1.ListJobsResponse{NextPageToken: next}
	for _, it := range items {
		out.Jobs = append(out.Jobs, listItemProto(it, c.OrgID))
	}
	return out, nil
}

// PublishJob publishes a job (or submits it for approval when the caller may not publish). Failed checks come
// back as published=false with the blocking gates. If the company cannot be checked (org down) the call fails
// with Unavailable and nothing is published.
func (s *RPC) PublishJob(ctx context.Context, req *hiringv1.PublishJobRequest) (*hiringv1.PublishJobResponse, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	res, err := svc.Publish(ctx, c, req.GetJobId(), 0)
	var blocked *draft.BlockedError
	if errors.As(err, &blocked) {
		out := &hiringv1.PublishJobResponse{}
		for _, b := range blocked.Report.Blocking {
			out.Blocks = append(out.Blocks, &hiringv1.GateBlock{Code: b.Code, GateId: b.GateID, Message: b.Message})
		}
		return out, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	return &hiringv1.PublishJobResponse{Published: res.Job.Status == jobs.StatusPublished, Job: jobProto(res.Job, c.OrgID)}, nil
}

func (s *RPC) transition(ctx context.Context, jobID, action string) (*hiringv1.Job, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	res, err := svc.Transition(ctx, c, jobID, action, 0)
	if err != nil {
		return nil, grpcError(err)
	}
	return jobProto(res.Job, c.OrgID), nil
}

// PauseJob pauses a published job.
func (s *RPC) PauseJob(ctx context.Context, req *hiringv1.PauseJobRequest) (*hiringv1.PauseJobResponse, error) {
	j, err := s.transition(ctx, req.GetJobId(), "pause")
	if err != nil {
		return nil, err
	}
	return &hiringv1.PauseJobResponse{Job: j}, nil
}

// ResumeJob resumes a paused job.
func (s *RPC) ResumeJob(ctx context.Context, req *hiringv1.ResumeJobRequest) (*hiringv1.ResumeJobResponse, error) {
	j, err := s.transition(ctx, req.GetJobId(), "resume")
	if err != nil {
		return nil, err
	}
	return &hiringv1.ResumeJobResponse{Job: j}, nil
}

// CloseJob closes a job. The reason is accepted but not stored: the job record has no field for it yet.
func (s *RPC) CloseJob(ctx context.Context, req *hiringv1.CloseJobRequest) (*hiringv1.CloseJobResponse, error) {
	j, err := s.transition(ctx, req.GetJobId(), "close")
	if err != nil {
		return nil, err
	}
	return &hiringv1.CloseJobResponse{Job: j}, nil
}

// GetPublicJob is the careers read. No CallContext needed. The frozen form travels as a JSON string.
func (s *RPC) GetPublicJob(ctx context.Context, req *hiringv1.GetPublicJobRequest) (*hiringv1.GetPublicJobResponse, error) {
	if s.Public == nil {
		return nil, status.Error(codes.Unavailable, "public jobs are not available")
	}
	v, err := s.Public.Load(ctx, req.GetPublicId())
	if err != nil {
		switch {
		case errors.Is(err, public.ErrNotFound):
			return nil, status.Error(codes.NotFound, "job not found")
		case errors.Is(err, public.ErrGone):
			return nil, status.Error(codes.NotFound, "this job is no longer available")
		}
		return nil, grpcError(err)
	}
	details, err := json.Marshal(v.Job)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	form, err := json.Marshal(v.Form)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal error")
	}
	state := strings.ToUpper(string(v.State))
	if state == "PAUSED" {
		state = "CLOSED" // the contract has OPEN, CLOSED or EXPIRED; a paused job takes no applications
	}
	out := &hiringv1.GetPublicJobResponse{
		Job: &hiringv1.Job{
			PublicId: v.PublicID, Title: v.Job.Title, WorkplaceType: v.Job.WorkplaceType, MarketCodes: v.Markets,
			RetentionMonths: int32(v.RetentionMonths), DetailsJson: string(details), JobNumber: 0,
		},
		CompanyName: v.Company.Name, PrivacyContactEmail: v.Company.PrivacyEmail, DpoContact: v.Company.DPOContact,
		FormVersion: int32(v.FormVersion), FormSchemaJson: string(form), PublicState: state,
	}
	if t, ok := parseDate(v.PublishedAt); ok {
		out.Job.PublishedAt = timestamppb.New(t)
	}
	if t, ok := parseDate(v.ClosingDate); ok {
		out.Job.ClosingAt = timestamppb.New(t)
	}
	return out, nil
}

// ListOpenJobs lists up to limit open public jobs, newest first, for the sitemap and careers listings.
func (s *RPC) ListOpenJobs(ctx context.Context, req *hiringv1.ListOpenJobsRequest) (*hiringv1.ListOpenJobsResponse, error) {
	if s.Public == nil {
		return nil, status.Error(codes.Unavailable, "public jobs are not available")
	}
	limit := int(req.GetLimit())
	if limit < 1 || limit > public.MaxSitemapURLs {
		limit = public.MaxSitemapURLs
	}
	list, err := s.Public.Sitemap(ctx, limit)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &hiringv1.ListOpenJobsResponse{}
	for _, j := range list {
		out.Jobs = append(out.Jobs, &hiringv1.OpenJob{PublicId: j.PublicID, UpdatedAt: timestamppb.New(j.UpdatedAt)})
	}
	return out, nil
}

// CheckJobAccess says whether the caller may see a job's applications (job.view.all, or job.view.assigned and
// the job's creator or a team member) and may change their statuses (also job.manage.any, the creator, a hiring
// manager or a recruiter), the same rules the applications service applies.
func (s *RPC) CheckJobAccess(ctx context.Context, req *hiringv1.CheckJobAccessRequest) (*hiringv1.CheckJobAccessResponse, error) {
	c, reg, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if s.Access == nil {
		return nil, status.Error(codes.Unavailable, "hiring is not available")
	}
	if !jobs.IsUUID(req.GetJobId()) {
		return &hiringv1.CheckJobAccessResponse{}, nil
	}
	createdBy, role, err := s.Access.JobTeam(ctx, reg, c.OrgID, req.GetJobId(), c.UserID)
	if errors.Is(err, hiringtypes.ErrNotFound) {
		return &hiringv1.CheckJobAccessResponse{}, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	see := c.Perms.Has(rbac.JobViewAll) || (c.Perms.Has(rbac.JobViewAssigned) && (createdBy == c.UserID || role != ""))
	manage := see && (c.Perms.Has(rbac.JobManageAny) || createdBy == c.UserID || role == "hiring_manager" || role == "recruiter")
	return &hiringv1.CheckJobAccessResponse{Allowed: see, CanManage: manage}, nil
}

// GetJobsSnapshot returns the current state of the company's jobs by id, for seeding and repairing the
// applications read model. Unknown ids and jobs the caller may not see are left out.
func (s *RPC) GetJobsSnapshot(ctx context.Context, req *hiringv1.GetJobsSnapshotRequest) (*hiringv1.GetJobsSnapshotResponse, error) {
	svc, c, err := s.scope(ctx)
	if err != nil {
		return nil, err
	}
	ids := req.GetJobIds()
	if len(ids) > maxSnapshotIDs {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d job ids per call", maxSnapshotIDs)
	}
	out := &hiringv1.GetJobsSnapshotResponse{}
	for _, id := range ids {
		v, gerr := svc.Get(ctx, c, id)
		if errors.Is(gerr, hiringtypes.ErrNotFound) {
			continue
		}
		if gerr != nil {
			return nil, grpcError(gerr)
		}
		out.Jobs = append(out.Jobs, jobProto(v, c.OrgID))
	}
	return out, nil
}

const maxSnapshotIDs = 100

var statusByEnum = map[hiringv1.JobStatus]string{
	hiringv1.JobStatus_JOB_STATUS_DRAFT:            jobs.StatusDraft,
	hiringv1.JobStatus_JOB_STATUS_PENDING_APPROVAL: jobs.StatusPendingApproval,
	hiringv1.JobStatus_JOB_STATUS_PUBLISHED:        jobs.StatusPublished,
	hiringv1.JobStatus_JOB_STATUS_PAUSED:           jobs.StatusPaused,
	hiringv1.JobStatus_JOB_STATUS_CLOSED:           jobs.StatusClosed,
	hiringv1.JobStatus_JOB_STATUS_ARCHIVED:         jobs.StatusArchived,
	hiringv1.JobStatus_JOB_STATUS_EXPIRED:          jobs.StatusExpired,
}

func statusSlug(s hiringv1.JobStatus) (string, bool) { v, ok := statusByEnum[s]; return v, ok }

func statusEnum(slug string) hiringv1.JobStatus {
	for e, s := range statusByEnum {
		if s == slug {
			return e
		}
	}
	return hiringv1.JobStatus_JOB_STATUS_UNSPECIFIED
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func jobProto(v *draft.JobView, companyID string) *hiringv1.Job {
	details, _ := json.Marshal(v.Details)
	j := &hiringv1.Job{
		Id: v.ID, CompanyId: companyID, JobNumber: int64(v.JobNumber), PublicId: v.PublicID, Title: v.Title,
		Status: statusEnum(v.Status), WorkplaceType: v.WorkplaceType, RetentionMonths: int32(v.RetentionMonths),
		CreatedBy: v.CreatedBy, DetailsJson: string(details),
	}
	seen := map[string]bool{}
	for _, m := range v.Markets {
		if !seen[m.MarketCode] {
			seen[m.MarketCode] = true
			j.MarketCodes = append(j.MarketCodes, m.MarketCode)
		}
	}
	for _, m := range v.Members {
		if m.Role == "hiring_manager" {
			j.HiringManagerIds = append(j.HiringManagerIds, m.UserID)
		}
	}
	if t, ok := parseDate(v.PublishedAt); ok {
		j.PublishedAt = timestamppb.New(t)
	}
	if t, ok := parseDate(v.ClosingDate); ok {
		j.ClosingAt = timestamppb.New(t)
	}
	return j
}

func listItemProto(it hiringtypes.ListItem, companyID string) *hiringv1.Job {
	j := &hiringv1.Job{
		Id: it.ID, CompanyId: companyID, Title: it.Title, Status: statusEnum(it.Status), WorkplaceType: it.WorkplaceType,
		MarketCodes: it.Markets, CreatedBy: it.CreatedBy,
	}
	for _, m := range it.Managers {
		j.HiringManagerIds = append(j.HiringManagerIds, m.UserID)
	}
	if t, ok := parseDate(it.PublishedAt); ok {
		j.PublishedAt = timestamppb.New(t)
	}
	if t, ok := parseDate(it.ClosingDate); ok {
		j.ClosingAt = timestamppb.New(t)
	}
	return j
}

// grpcError maps a service error to a gRPC status. Anything unexpected is Internal with no detail, so storage
// errors never reach the caller.
func grpcError(err error) error {
	if _, ok := status.FromError(err); ok && status.Code(err) != codes.Unknown {
		return err
	}
	var ae *apperror.AppError
	if !errors.As(err, &ae) {
		err = hiringhttp.APIError(err)
	}
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperror.CodeNotFound:
			return status.Error(codes.NotFound, ae.Message)
		case apperror.CodeForbidden:
			return status.Error(codes.PermissionDenied, ae.Message)
		case apperror.CodeValidationError, apperror.CodeInvalidReference:
			return status.Error(codes.InvalidArgument, ae.Message)
		case apperror.CodeConflict, apperror.CodePreconditionRequired, apperror.CodePublishBlocked:
			return status.Error(codes.FailedPrecondition, ae.Message)
		case apperror.CodeUnauthorized:
			return status.Error(codes.Unauthenticated, ae.Message)
		case apperror.CodeServiceUnavailable:
			return status.Error(codes.Unavailable, ae.Message)
		}
	}
	return status.Error(codes.Internal, "internal error")
}
