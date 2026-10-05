package kyb

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"
)

type memStore struct {
	rec    *Record
	events []Event
	saves  int
}

func (m *memStore) Load(_ context.Context, _ string) (*Record, error) {
	if m.rec == nil {
		return nil, nil
	}
	c := *m.rec // return a copy so a failed run cannot mutate stored state
	return &c, nil
}
func (m *memStore) Save(_ context.Context, rec *Record, ev ...Event) error {
	c := *rec
	m.rec = &c
	m.events = append(m.events, ev...)
	m.saves++
	return nil
}

type memQueue struct {
	pending  map[string]bool
	resolved map[string]bool
}

func (q *memQueue) Enqueue(_ context.Context, org, _ string) error { q.pending[org] = true; return nil }
func (q *memQueue) Resolve(_ context.Context, org, _ string, approved bool) error {
	delete(q.pending, org)
	q.resolved[org] = approved
	return nil
}

type memNotifier struct {
	sent []Notice
	err  error
}

func (n *memNotifier) Notify(_ context.Context, _ string, notice Notice, _ FailureReason) error {
	n.sent = append(n.sent, notice)
	return n.err
}

// stubVerifier returns whatever the test sets, and records the last request.
type stubVerifier struct {
	res  Result
	err  error
	last Request
}

func (s *stubVerifier) Verify(_ context.Context, r *Request) (Result, error) {
	s.last = *r
	return s.res, s.err
}

const registryTradingName = "ACME TRADING LIMITED"

type fixture struct {
	svc  *Service
	st   *memStore
	q    *memQueue
	n    *memNotifier
	stub *stubVerifier
	now  time.Time
}

func newFixture() *fixture {
	f := &fixture{st: &memStore{}, q: &memQueue{pending: map[string]bool{}, resolved: map[string]bool{}},
		n: &memNotifier{}, stub: &stubVerifier{}, now: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	reg := NewRegistry()
	reg.Register("GB", f.stub)
	f.svc = &Service{Store: f.st, Queue: f.q, Registry: reg, Notifier: f.n, Now: func() time.Time { return f.now }}
	return f
}

// mustSubmit runs Submit and fails the test on error.
func mustSubmit(t *testing.T, f *fixture, in *SubmitInput) *Record {
	t.Helper()
	rec, err := f.svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return rec
}

func ukInput() *SubmitInput {
	return &SubmitInput{OrganizationID: "org1", CountryCode: "gb", RegistrationNumber: "12345678", LegalName: "Acme Ltd",
		Address: Address{Line1: "1 High Street", PostCode: "EC1A 1BB"}, ActorID: "user1"}
}

func TestSubmitVerifiedTier1(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeVerified}
	rec, err := f.svc.Submit(context.Background(), ukInput())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusVerified || rec.VerifiedAt == nil || rec.Tier != Tier1 || rec.CountryCode != "GB" || rec.Attempts != 1 {
		t.Fatalf("unexpected record %+v", rec)
	}
	if !rec.Status.CanPublish() || len(f.n.sent) != 0 || len(f.q.pending) != 0 {
		t.Fatal("verified org should publish, send no email and not be queued")
	}
	if last := f.st.events[len(f.st.events)-1]; last.Name != "verified" {
		t.Fatalf("last event = %s", last.Name)
	}
}

func TestSubmitTier2GoesToReviewQueue(t *testing.T) {
	f := newFixture()
	in := ukInput()
	in.CountryCode, in.RegistrationNumber = "NG", "RC1234567"
	rec, err := f.svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusPending || rec.Tier != Tier2 || !f.q.pending["org1"] {
		t.Fatalf("expected pending tier 2 in queue, got %+v", rec)
	}
	if len(f.n.sent) != 1 || f.n.sent[0] != NoticeReviewQueue {
		t.Fatalf("expected review-queued email, got %v", f.n.sent)
	}
	if rec.Status.CanPublish() || !rec.Status.CanDraft() {
		t.Fatal("pending must allow drafting but not publishing")
	}
	if _, err := f.svc.Submit(context.Background(), in); !errors.Is(err, ErrReviewInProgress) {
		t.Fatalf("resubmitting during review should be blocked, got %v", err)
	}
}

func TestNumberNotFoundThenLightRetry(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}
	rec := mustSubmit(t, f, ukInput())
	if rec.Status != StatusFailed || rec.FailureReason != ReasonNumberNotFound {
		t.Fatalf("got %+v", rec)
	}
	if len(f.n.sent) != 1 || f.n.sent[0] != NoticeFailed {
		t.Fatalf("expected failed email, got %v", f.n.sent)
	}

	f.stub.res = Result{Outcome: OutcomeVerified}
	rec, err := f.svc.Resubmit(context.Background(), "org1", "Acme Ltd", "87654321", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusVerified || rec.RegistrationNumber != "87654321" || rec.Attempts != 2 {
		t.Fatalf("got %+v", rec)
	}
	// The light form must not clear or re-ask the address.
	if f.stub.last.Address.PostCode != "EC1A 1BB" || f.stub.last.CountryCode != "GB" {
		t.Fatalf("retry lost address or country: %+v", f.stub.last)
	}
}

func TestNameMismatchConfirmNameReRunsCheck(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonNameMismatch, RegistryName: registryTradingName}
	rec := mustSubmit(t, f, ukInput())
	if rec.FailureReason != ReasonNameMismatch || rec.RegistryName != registryTradingName {
		t.Fatalf("got %+v", rec)
	}
	f.stub.res = Result{Outcome: OutcomeVerified}
	rec, err := f.svc.ConfirmName(context.Background(), "org1", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if f.stub.last.LegalName != registryTradingName || rec.LegalName != registryTradingName || rec.Status != StatusVerified {
		t.Fatalf("confirm-name should re-check with the registry name, got %+v / %+v", f.stub.last, rec)
	}
}

func TestConfirmNameOnlyForNameMismatch(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}
	mustSubmit(t, f, ukInput())
	if _, err := f.svc.ConfirmName(context.Background(), "org1", "u"); !errors.Is(err, ErrActionNotAllowed) {
		t.Fatalf("got %v", err)
	}
}

func TestAddressMismatchIsSoftWarning(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonAddressMismatch}
	rec := mustSubmit(t, f, ukInput())
	if rec.FailureReason != ReasonAddressMismatch || DisplayState(rec.Status, rec.FailureReason) != DisplayActionRequired {
		t.Fatalf("got %+v", rec)
	}
	if _, err := f.svc.ConfirmAddress(context.Background(), "org1", "home", "u"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid address type should be rejected, got %v", err)
	}
	rec, err := f.svc.ConfirmAddress(context.Background(), "org1", AddressTrading, "user1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusVerified || rec.AddressType != AddressTrading || rec.FailureReason != "" {
		t.Fatalf("got %+v", rec)
	}
}

func TestInactiveEntityIsHardBlock(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonInactiveEntity}
	rec := mustSubmit(t, f, ukInput())
	if DisplayState(rec.Status, rec.FailureReason) != DisplayFailed {
		t.Fatalf("got %+v", rec)
	}
	ctx := context.Background()
	if _, err := f.svc.Submit(ctx, ukInput()); !errors.Is(err, ErrResubmissionBlocked) {
		t.Errorf("Submit: %v", err)
	}
	if _, err := f.svc.Resubmit(ctx, "org1", "Acme", "1", "u"); !errors.Is(err, ErrResubmissionBlocked) {
		t.Errorf("Resubmit: %v", err)
	}
	if _, err := f.svc.ChangeCountry(ctx, "org1", "IE", "u"); !errors.Is(err, ErrActionNotAllowed) {
		t.Errorf("ChangeCountry: %v", err)
	}
}

func TestWrongCountryDetectedByFormatAndChangeCountry(t *testing.T) {
	f := newFixture()
	ukFormat := regexp.MustCompile(`^(\d{8}|(SC|NI)\d{6})$`)
	f.svc.Formats = func(country, number string) bool { return country != "GB" || ukFormat.MatchString(number) }
	in := ukInput()
	in.RegistrationNumber = "RC1234567" // a Nigerian-shaped number
	rec, err := f.svc.Submit(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if rec.FailureReason != ReasonWrongCountry {
		t.Fatalf("got %+v", rec)
	}
	if f.stub.last.RegistrationNumber != "" {
		t.Fatal("a wrong-country number must not reach the registry")
	}
	rec, err = f.svc.ChangeCountry(context.Background(), "org1", "ng", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CountryCode != "NG" || rec.RegistrationNumber != "" || rec.Status != StatusNotStarted ||
		rec.LegalName != "Acme Ltd" || rec.Address.PostCode != "EC1A 1BB" || rec.Tier != Tier2 {
		t.Fatalf("change country should clear the number only, got %+v", rec)
	}
}

func TestTransientRegistryErrorChangesNothing(t *testing.T) {
	f := newFixture()
	f.stub.err = ErrRateLimited
	if _, err := f.svc.Submit(context.Background(), ukInput()); !errors.Is(err, ErrTemporarilyUnavailable) {
		t.Fatalf("got %v", err)
	}
	if f.st.saves != 0 || len(f.n.sent) != 0 {
		t.Fatal("a registry that could not be reached must not save state or send email")
	}
}

func TestAlreadyVerifiedCannotResubmit(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeVerified}
	mustSubmit(t, f, ukInput())
	if _, err := f.svc.Submit(context.Background(), ukInput()); !errors.Is(err, ErrAlreadyVerified) {
		t.Fatalf("got %v", err)
	}
}

func TestSubmitValidation(t *testing.T) {
	f := newFixture()
	for _, mod := range []func(*SubmitInput){
		func(i *SubmitInput) { i.OrganizationID = "" },
		func(i *SubmitInput) { i.CountryCode = "GBR" },
		func(i *SubmitInput) { i.RegistrationNumber = "  " },
		func(i *SubmitInput) { i.LegalName = "" },
		func(i *SubmitInput) { i.AddressType = "mailing" },
	} {
		in := ukInput()
		mod(in)
		if _, err := f.svc.Submit(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("expected ErrInvalidInput, got %v", err)
		}
	}
}

// queuedTier2 returns a fixture with a Kenyan submission waiting in the review queue.
func queuedTier2(t *testing.T) *fixture {
	t.Helper()
	f := newFixture()
	in := ukInput()
	in.CountryCode, in.RegistrationNumber = "KE", "PVT-ABC123"
	mustSubmit(t, f, in)
	f.n.sent = nil
	return f
}

func TestReviewApprove(t *testing.T) {
	f := queuedTier2(t)
	rec, err := f.svc.Review(context.Background(), "org1", "op1", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusVerified || rec.ReviewerID != "op1" {
		t.Fatalf("approve: %+v", rec)
	}
	if f.q.pending["org1"] || !f.q.resolved["org1"] {
		t.Fatal("queue item should be resolved as approved")
	}
}

func TestReviewRejectNeedsReasonAndEmails(t *testing.T) {
	ctx := context.Background()
	f := queuedTier2(t)
	if _, err := f.svc.Review(ctx, "org1", "op1", false, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("a rejection needs a reason, got %v", err)
	}
	rec, err := f.svc.Review(ctx, "org1", "op1", false, ReasonNameMismatch)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusFailed || rec.FailureReason != ReasonNameMismatch {
		t.Fatalf("reject: %+v", rec)
	}
	if len(f.n.sent) != 1 || f.n.sent[0] != NoticeFailed {
		t.Fatalf("expected one failed email, got %v", f.n.sent)
	}
	if f.q.pending["org1"] || f.q.resolved["org1"] {
		t.Fatal("queue item should be resolved as rejected")
	}
}

func TestReviewOnlyAppliesToPendingTier2(t *testing.T) {
	f := newFixture()
	f.stub.res = Result{Outcome: OutcomeVerified}
	mustSubmit(t, f, ukInput())
	if _, err := f.svc.Review(context.Background(), "org1", "op1", true, ""); !errors.Is(err, ErrActionNotAllowed) {
		t.Fatalf("got %v", err)
	}
}

func TestNotifierFailureDoesNotFailVerification(t *testing.T) {
	f := newFixture()
	f.n.err = errors.New("smtp down")
	f.stub.res = Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}
	rec, err := f.svc.Submit(context.Background(), ukInput())
	if err != nil || rec.Status != StatusFailed {
		t.Fatalf("got %+v err %v", rec, err)
	}
}

func TestNudgeCadenceAndDeletion(t *testing.T) {
	failedAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	rec := &Record{Status: StatusFailed, CheckedAt: &failedAt}
	cases := []struct {
		name string
		sent int
		age  time.Duration
		want Notice
		ok   bool
	}{
		{"too early", 0, 47 * time.Hour, "", false},
		{"nudge 1 at 2 days", 0, 48 * time.Hour, NoticeNudge1, true},
		{"nudge 1 not repeated", 1, 3 * 24 * time.Hour, "", false},
		{"nudge 2 at 7 days", 1, 7 * 24 * time.Hour, NoticeNudge2, true},
		{"nothing after nudge 2", 2, 20 * 24 * time.Hour, "", false},
	}
	for _, c := range cases {
		rec.NoticesSent = c.sent
		got, ok := NextNotice(rec, failedAt.Add(c.age))
		if got != c.want || ok != c.ok {
			t.Errorf("%s: got %q,%v want %q,%v", c.name, got, ok, c.want, c.ok)
		}
	}
	if DeletionDue(rec, failedAt.Add(29*24*time.Hour)) || !DeletionDue(rec, failedAt.Add(30*24*time.Hour)) {
		t.Error("deletion should be due at 30 days, not before")
	}
	for _, s := range []Status{StatusVerified, StatusPending, StatusNotStarted} {
		rec.Status = s
		if _, ok := NextNotice(rec, failedAt.Add(10*24*time.Hour)); ok {
			t.Errorf("status %s must not get nudges", s)
		}
		if DeletionDue(rec, failedAt.Add(60*24*time.Hour)) {
			t.Errorf("status %s must not be flagged for deletion", s)
		}
	}
}
