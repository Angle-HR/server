package applicationsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
)

// Applier is what SubmitApplication needs of apply.Service.
type Applier interface {
	Submit(ctx context.Context, in apply.Input) (*apply.Receipt, error)
}

// Uploader is what CreateUploadUrl and ConfirmUpload need of uploads.Service.
type Uploader interface {
	Presign(ctx context.Context, in uploads.PresignInput) (*uploads.Presigned, error)
	Verify(ctx context.Context, in uploads.VerifyInput) (apply.File, error)
}

var (
	_ Applier  = (*apply.Service)(nil)
	_ Uploader = (*uploads.Service)(nil)
)

// SubmitApplication takes a public application, the way POST /public/jobs/{public_id}/applications does. The
// receipt is the same for a new application and a repeat.
func (s *RPC) SubmitApplication(ctx context.Context, req *applicationsv1.SubmitApplicationRequest) (*applicationsv1.SubmitApplicationResponse, error) {
	if s.Apply == nil {
		return nil, status.Error(codes.Unavailable, "applications are not available")
	}
	answers := make(map[string]json.RawMessage, len(req.GetAnswers()))
	for id, raw := range req.GetAnswers() {
		if !json.Valid([]byte(raw)) {
			return nil, status.Errorf(codes.InvalidArgument, "answers[%s] is not valid JSON", id)
		}
		answers[id] = json.RawMessage(raw)
	}
	rec, err := s.Apply.Submit(ctx, apply.Input{
		PublicID:           req.GetPublicJobId(),
		CandidateCountry:   req.GetCandidateCountry(),
		Answers:            answers,
		NoticeAcknowledged: req.GetPrivacyNoticeAcknowledged(),
		Consent:            req.GetConsent(),
		Source:             req.GetSource(),
		IdempotencyKey:     req.GetIdempotencyKey(),
		ClientIP:           req.GetClientIp(),
	})
	if err != nil {
		return nil, applyGRPCError(err)
	}
	return &applicationsv1.SubmitApplicationResponse{Received: rec.Received}, nil
}

// CreateUploadUrl issues the presigned target for one file question of a public job.
func (s *RPC) CreateUploadUrl(ctx context.Context, req *applicationsv1.CreateUploadUrlRequest) (*applicationsv1.CreateUploadUrlResponse, error) {
	if s.Uploads == nil {
		return nil, applyGRPCError(apply.ErrFilesUnavailable)
	}
	p, err := s.Uploads.Presign(ctx, uploads.PresignInput{
		PublicID: req.GetPublicJobId(), QuestionID: req.GetQuestionId(), Filename: req.GetFilename(),
		ContentType: req.GetContentType(), SizeBytes: req.GetSizeBytes(), ClientIP: req.GetClientIp(),
	})
	if err != nil {
		return nil, applyGRPCError(err)
	}
	return &applicationsv1.CreateUploadUrlResponse{
		Token: p.Token, Method: p.Method, Url: p.URL, Headers: p.Headers,
		ExpiresAt: timestamppb.New(p.ExpiresAt), MaxBytes: p.MaxBytes,
	}, nil
}

// ConfirmUpload checks an uploaded file ahead of the submit (see the proto comment).
func (s *RPC) ConfirmUpload(ctx context.Context, req *applicationsv1.ConfirmUploadRequest) (*applicationsv1.ConfirmUploadResponse, error) {
	if s.Uploads == nil {
		return nil, applyGRPCError(apply.ErrFilesUnavailable)
	}
	f, err := s.Uploads.Verify(ctx, uploads.VerifyInput{
		PublicID: req.GetPublicJobId(), QuestionID: req.GetQuestionId(), Token: req.GetToken(),
	})
	if err != nil {
		return nil, applyGRPCError(err)
	}
	return &applicationsv1.ConfirmUploadResponse{SizeBytes: f.SizeBytes, Sha256: f.SHA256}, nil
}

// applyGRPCError maps the errors of the apply and upload steps. A validation error carries every field problem as
// a BadRequest detail.
func applyGRPCError(err error) error {
	var ve *apply.ValidationError
	var rl *apply.RateLimitedError
	switch {
	case errors.As(err, &ve):
		st := status.New(codes.InvalidArgument, fmt.Sprintf("%d fields need attention", len(ve.Fields)))
		br := &errdetails.BadRequest{}
		fields := append([]apply.FieldError(nil), ve.Fields...)
		sort.SliceStable(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
		for _, f := range fields {
			br.FieldViolations = append(br.FieldViolations, &errdetails.BadRequest_FieldViolation{Field: f.Path, Description: f.Message})
		}
		if d, derr := st.WithDetails(br); derr == nil {
			st = d
		}
		return st.Err()
	case errors.As(err, &rl), errors.Is(err, apply.ErrRateLimited):
		return status.Error(codes.ResourceExhausted, "too many requests, try again later")
	case errors.Is(err, apply.ErrNotFound):
		return status.Error(codes.NotFound, "job not found")
	case errors.Is(err, apply.ErrGone):
		return status.Error(codes.NotFound, "this job is no longer available")
	case errors.Is(err, apply.ErrNotAccepting):
		return status.Error(codes.FailedPrecondition, "this job is not accepting applications")
	case errors.Is(err, apply.ErrIdempotencyKeyReused):
		return status.Error(codes.AlreadyExists, "this idempotency key was already used for a different application")
	case errors.Is(err, apply.ErrFileRejected):
		return status.Error(codes.InvalidArgument, "this file could not be used; upload it again")
	case errors.Is(err, apply.ErrFilesUnavailable):
		return status.Error(codes.Unavailable, "file uploads are not available right now")
	case errors.Is(err, apply.ErrSensitiveUnavailable):
		return status.Error(codes.Unavailable, "this application asks for sensitive information, which cannot be accepted yet")
	}
	return grpcError(err)
}
