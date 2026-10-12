package kyb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRequirePublish(t *testing.T) {
	for _, s := range []Status{StatusNotStarted, StatusPending, StatusFailed} {
		if err := RequirePublish(s); !errors.Is(err, ErrNotVerified) {
			t.Errorf("%s: got %v, want ErrNotVerified", s, err)
		}
	}
	if err := RequirePublish(StatusVerified); err != nil {
		t.Errorf("verified: got %v", err)
	}
	for _, s := range []Status{StatusNotStarted, StatusPending, StatusFailed, StatusVerified} {
		if !s.CanDraft() {
			t.Errorf("%s: drafting must never be blocked", s)
		}
	}
}

func TestRequirePublishFor(t *testing.T) {
	ctx := context.Background()
	if err := RequirePublishFor(ctx, &memStore{}, "org"); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("no record: got %v", err)
	}
	if err := RequirePublishFor(ctx, &memStore{rec: &Record{Status: StatusVerified}}, "org"); err != nil {
		t.Fatalf("verified: got %v", err)
	}
}

func transferFixture(status Status) *fixture {
	f := newFixture()
	at := f.now.Add(-time.Hour)
	f.st.rec = &Record{OrganizationID: "org", CountryCode: "GB", RegistrationNumber: "01234567",
		LegalName: "Acme Ltd", Status: status, VerifiedAt: &at, ReviewerID: "rev", NoticesSent: 1}
	return f
}

func TestOwnershipTransferred_verifiedMustReverify(t *testing.T) {
	f := transferFixture(StatusVerified)
	rec, err := f.svc.OwnershipTransferred(context.Background(), "org", "new-owner", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusNotStarted || rec.VerifiedAt != nil || rec.ReviewerID != "" || rec.NoticesSent != 0 {
		t.Fatalf("record not reset: %+v", rec)
	}
	if rec.RegistrationNumber != "01234567" || rec.LegalName != "Acme Ltd" {
		t.Fatalf("details should be kept for pre-filling: %+v", rec)
	}
	if len(f.st.events) != 1 || f.st.events[0].Name != "ownership_transferred" {
		t.Fatalf("events: %+v", f.st.events)
	}
	if err := RequirePublish(rec.Status); !errors.Is(err, ErrNotVerified) {
		t.Fatalf("new owner must not publish before re-verifying: %v", err)
	}
}

func TestOwnershipTransferred_failedIsReset(t *testing.T) {
	f := transferFixture(StatusFailed)
	f.st.rec.FailureReason = ReasonNumberNotFound
	rec, err := f.svc.OwnershipTransferred(context.Background(), "org", "new-owner", "admin")
	if err != nil || rec.Status != StatusNotStarted || rec.FailureReason != "" {
		t.Fatalf("got %+v, %v", rec, err)
	}
}

func TestOwnershipTransferred_pendingAndMissingAreLeftAlone(t *testing.T) {
	f := transferFixture(StatusPending)
	rec, err := f.svc.OwnershipTransferred(context.Background(), "org", "new-owner", "admin")
	if err != nil || rec.Status != StatusPending || f.st.saves != 0 {
		t.Fatalf("pending: got %+v, %v, saves %d", rec, err, f.st.saves)
	}

	empty := newFixture()
	rec, err = empty.svc.OwnershipTransferred(context.Background(), "org", "new-owner", "admin")
	if err != nil || rec != nil || empty.st.saves != 0 {
		t.Fatalf("missing: got %+v, %v", rec, err)
	}
}

func TestOwnershipTransferred_invalidInput(t *testing.T) {
	f := newFixture()
	if _, err := f.svc.OwnershipTransferred(context.Background(), "org", "", "admin"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("got %v", err)
	}
}
