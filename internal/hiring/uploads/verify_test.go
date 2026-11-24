package uploads_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
)

func TestVerifyScansEarlyAndSubmitDoesNotScanAgain(t *testing.T) {
	e := setup(target())
	p := presignCV(t, e, 2000)
	put(e, p, pdf(2000-9))

	f, err := e.svc.Verify(context.Background(), uploads.VerifyInput{PublicID: "jabc", QuestionID: "cv", Token: p.Token})
	if err != nil || len(f.SHA256) != 64 || f.QuestionID != "cv" {
		t.Fatalf("verify: %+v %v", f, err)
	}
	if _, err = e.svc.Claim(context.Background(), claimReq(e, p.Token)); err != nil {
		t.Fatalf("claim after verify: %v", err)
	}
	if len(e.sc.scanned) != 1 {
		t.Errorf("scanned %d times, want once", len(e.sc.scanned))
	}
}

func TestVerifyRefusals(t *testing.T) {
	e := setup(target())
	p := presignCV(t, e, 2000)

	// Nothing uploaded yet: rejected, the candidate may still PUT to the same URL.
	_, err := e.svc.Verify(context.Background(), uploads.VerifyInput{PublicID: "jabc", QuestionID: "cv", Token: p.Token})
	if !errors.Is(err, apply.ErrFileRejected) {
		t.Fatalf("missing object: %v", err)
	}
	// Not a file question.
	_, err = e.svc.Verify(context.Background(), uploads.VerifyInput{PublicID: "jabc", QuestionID: "name", Token: p.Token})
	if fieldPath(err) != "question_id" {
		t.Fatalf("non-file question: %v", err)
	}
	// Unknown job.
	_, err = e.svc.Verify(context.Background(), uploads.VerifyInput{PublicID: "nope", QuestionID: "cv", Token: p.Token})
	if !errors.Is(err, apply.ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
	// Infected file is rejected for good.
	e2 := setup(target())
	e2.sc.verdict.Clean = false
	p2 := presignCV(t, e2, 2000)
	put(e2, p2, bytes.Clone(pdf(2000-9)))
	if _, err = e2.svc.Verify(context.Background(), uploads.VerifyInput{PublicID: "jabc", QuestionID: "cv", Token: p2.Token}); !errors.Is(err, apply.ErrFileRejected) {
		t.Fatalf("infected: %v", err)
	}
}
