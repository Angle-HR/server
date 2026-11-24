package applicationsvc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
)

type fakeApplier struct {
	got apply.Input
	err error
}

func (f *fakeApplier) Submit(_ context.Context, in apply.Input) (*apply.Receipt, error) {
	f.got = in
	if f.err != nil {
		return nil, f.err
	}
	return &apply.Receipt{Received: true}, nil
}

type fakeUploader struct {
	presign uploads.PresignInput
	verify  uploads.VerifyInput
	err     error
}

func (f *fakeUploader) Presign(_ context.Context, in uploads.PresignInput) (*uploads.Presigned, error) {
	f.presign = in
	if f.err != nil {
		return nil, f.err
	}
	return &uploads.Presigned{
		Token: "tok", Method: "PUT", URL: "http://store/u", Headers: map[string]string{"Content-Type": "application/pdf"},
		ExpiresAt: time.Unix(1700000900, 0).UTC(), MaxBytes: 5 << 20,
	}, nil
}

func (f *fakeUploader) Verify(_ context.Context, in uploads.VerifyInput) (apply.File, error) {
	f.verify = in
	if f.err != nil {
		return apply.File{}, f.err
	}
	return apply.File{ID: in.Token, SizeBytes: 12, SHA256: "abc"}, nil
}

// anonymous is a call with no CallContext user: the public calls need none.
func anonymous(ctx func(*commonv1.CallContext) context.Context) context.Context {
	return ctx(&commonv1.CallContext{})
}

func TestSubmitApplicationPassesTheRealFlowFields(t *testing.T) {
	ap := &fakeApplier{}
	c, ctx := startRPC(t, &RPC{Apply: ap})
	res, err := c.SubmitApplication(anonymous(ctx), &applicationsv1.SubmitApplicationRequest{
		PublicJobId: "jabc", CandidateCountry: "GB", PrivacyNoticeAcknowledged: true, Consent: true, Source: "linkedin",
		IdempotencyKey: "key-12345678", ClientIp: "203.0.113.9",
		Answers: map[string]string{"q_email": `"ada@example.com"`, "q_cv": `"7b2c"`, "q_years": `5`},
	})
	if err != nil || !res.Received {
		t.Fatalf("%v %+v", err, res)
	}
	g := ap.got
	if g.PublicID != "jabc" || g.CandidateCountry != "GB" || !g.NoticeAcknowledged || !g.Consent || g.Source != "linkedin" ||
		g.IdempotencyKey != "key-12345678" || g.ClientIP != "203.0.113.9" {
		t.Fatalf("input: %+v", g)
	}
	var years int
	if err := json.Unmarshal(g.Answers["q_years"], &years); err != nil || years != 5 || string(g.Answers["q_email"]) != `"ada@example.com"` {
		t.Fatalf("answers not passed as raw JSON by question id: %v", g.Answers)
	}
}

func TestSubmitApplicationRejectsInvalidAnswerJSON(t *testing.T) {
	c, ctx := startRPC(t, &RPC{Apply: &fakeApplier{}})
	_, err := c.SubmitApplication(anonymous(ctx), &applicationsv1.SubmitApplicationRequest{Answers: map[string]string{"q": "{nope"}})
	wantCode(t, err, codes.InvalidArgument)
}

func TestSubmitApplicationErrorMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{apply.ErrNotFound, codes.NotFound},
		{apply.ErrGone, codes.NotFound},
		{apply.ErrNotAccepting, codes.FailedPrecondition},
		{apply.ErrIdempotencyKeyReused, codes.AlreadyExists},
		{apply.ErrRateLimited, codes.ResourceExhausted},
		{&apply.RateLimitedError{RetryAfter: time.Minute}, codes.ResourceExhausted},
		{apply.ErrFilesUnavailable, codes.Unavailable},
		{apply.ErrSensitiveUnavailable, codes.Unavailable},
		{apply.ErrFileRejected, codes.InvalidArgument},
	} {
		c, ctx := startRPC(t, &RPC{Apply: &fakeApplier{err: tc.err}})
		_, err := c.SubmitApplication(anonymous(ctx), &applicationsv1.SubmitApplicationRequest{PublicJobId: "x"})
		if status.Code(err) != tc.want {
			t.Errorf("%v: got %v want %v", tc.err, status.Code(err), tc.want)
		}
	}
}

func TestSubmitApplicationValidationErrorCarriesFields(t *testing.T) {
	ve := &apply.ValidationError{Fields: []apply.FieldError{{Path: "answers.q_email", Message: "enter a valid email"}, {Path: "candidate_country", Message: "not in this job's markets"}}}
	c, ctx := startRPC(t, &RPC{Apply: &fakeApplier{err: ve}})
	_, err := c.SubmitApplication(anonymous(ctx), &applicationsv1.SubmitApplicationRequest{})
	wantCode(t, err, codes.InvalidArgument)
	var br *errdetails.BadRequest
	for _, d := range status.Convert(err).Details() {
		if b, ok := d.(*errdetails.BadRequest); ok {
			br = b
		}
	}
	if br == nil || len(br.FieldViolations) != 2 || br.FieldViolations[0].Field != "answers.q_email" {
		t.Fatalf("details: %+v", br)
	}
}

func TestUploadCallsAreUnavailableWithoutAScanner(t *testing.T) {
	c, ctx := startRPC(t, &RPC{})
	_, e1 := c.CreateUploadUrl(anonymous(ctx), &applicationsv1.CreateUploadUrlRequest{})
	_, e2 := c.ConfirmUpload(anonymous(ctx), &applicationsv1.ConfirmUploadRequest{})
	_, e3 := c.SubmitApplication(anonymous(ctx), &applicationsv1.SubmitApplicationRequest{})
	for _, err := range []error{e1, e2, e3} {
		wantCode(t, err, codes.Unavailable)
	}
}

func TestCreateUploadUrlAndConfirmUpload(t *testing.T) {
	up := &fakeUploader{}
	c, ctx := startRPC(t, &RPC{Uploads: up})
	res, err := c.CreateUploadUrl(anonymous(ctx), &applicationsv1.CreateUploadUrlRequest{
		PublicJobId: "jabc", QuestionId: "q_cv", Filename: "cv.pdf", ContentType: "application/pdf", SizeBytes: 1234, ClientIp: "203.0.113.9",
	})
	if err != nil || res.Token != "tok" || res.Method != "PUT" || res.Url != "http://store/u" ||
		res.Headers["Content-Type"] != "application/pdf" || res.MaxBytes != 5<<20 || res.ExpiresAt.AsTime().Unix() != 1700000900 {
		t.Fatalf("%v %+v", err, res)
	}
	if up.presign.PublicID != "jabc" || up.presign.QuestionID != "q_cv" || up.presign.SizeBytes != 1234 || up.presign.ClientIP != "203.0.113.9" {
		t.Fatalf("presign input: %+v", up.presign)
	}
	conf, err := c.ConfirmUpload(anonymous(ctx), &applicationsv1.ConfirmUploadRequest{PublicJobId: "jabc", QuestionId: "q_cv", Token: "tok"})
	if err != nil || conf.SizeBytes != 12 || conf.Sha256 != "abc" || up.verify.Token != "tok" || up.verify.QuestionID != "q_cv" {
		t.Fatalf("%v %+v %+v", err, conf, up.verify)
	}
}

func TestUploadErrorsMap(t *testing.T) {
	c, ctx := startRPC(t, &RPC{Uploads: &fakeUploader{err: apply.ErrFileRejected}})
	_, err := c.ConfirmUpload(anonymous(ctx), &applicationsv1.ConfirmUploadRequest{})
	wantCode(t, err, codes.InvalidArgument)
	c, ctx = startRPC(t, &RPC{Uploads: &fakeUploader{err: apply.ErrNotAccepting}})
	_, err = c.CreateUploadUrl(anonymous(ctx), &applicationsv1.CreateUploadUrlRequest{})
	wantCode(t, err, codes.FailedPrecondition)
}
