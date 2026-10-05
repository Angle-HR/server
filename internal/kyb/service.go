package kyb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Errors returned by Service for conditions the caller should map to an API response.
var (
	ErrInvalidInput           = errors.New("kyb: invalid input")
	ErrAlreadyVerified        = errors.New("kyb: organization is already verified")
	ErrResubmissionBlocked    = errors.New("kyb: entity is inactive, contact support")
	ErrReviewInProgress       = errors.New("kyb: manual review is in progress")
	ErrNothingToRetry         = errors.New("kyb: no failed verification to retry")
	ErrActionNotAllowed       = errors.New("kyb: action not allowed for the current verification state")
	ErrTemporarilyUnavailable = errors.New("kyb: verification temporarily unavailable, try again")
)

// Event is one row of the append-only accounts.organization_verification_events history.
type Event struct {
	OrganizationID string
	Name           string // submitted, verified, failed, queued_for_review, review_approved, ...
	Status         Status
	Reason         FailureReason
	ActorID        string
	Detail         map[string]any
	At             time.Time
}

// Store persists verification state. Save must write the record, update
// accounts.organizations.kyb_status (and kyb_verified_at) and append the events
// in one transaction, and write the audit log entry.
type Store interface {
	// Load returns the record, or nil, nil when the organization has none yet.
	Load(ctx context.Context, organizationID string) (*Record, error)
	Save(ctx context.Context, rec *Record, events ...Event) error
}

// Queue is the global review queue for Tier 2 markets (admin.verification_queue).
type Queue interface {
	Enqueue(ctx context.Context, organizationID, countryCode string) error
	// Resolve closes the pending item for the organization.
	Resolve(ctx context.Context, organizationID, reviewerID string, approved bool) error
}

// Notice is an email in the lifecycle of KYB V2 §5.4.
type Notice string

// Notices in the KYB V2 §5.4 email lifecycle.
const (
	NoticeFailed      Notice = "kyb_failed"        // immediately, names the specific reason
	NoticeReviewQueue Notice = "kyb_review_queued" // immediately, reassuring
	NoticeNudge1      Notice = "kyb_nudge_1"       // +2 days unresolved
	NoticeNudge2      Notice = "kyb_nudge_2"       // +7 days unresolved, mentions support
)

// Notifier sends lifecycle emails. Errors are logged by the caller and never fail a verification.
type Notifier interface {
	Notify(ctx context.Context, organizationID string, notice Notice, reason FailureReason) error
}

// FormatChecker reports whether a registration number has the right shape for a
// country. It is how a "wrong country selected" failure is detected without calling a registry.
type FormatChecker func(countryCode, registrationNumber string) bool

// Service runs the verification flow.
type Service struct {
	Store    Store
	Queue    Queue
	Registry *Registry
	Notifier Notifier      // optional
	Formats  FormatChecker // optional
	Logger   *slog.Logger  // optional, defaults to slog.Default()
	Now      func() time.Time
}

func (s *Service) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// SubmitInput is the full first-submission form.
type SubmitInput struct {
	OrganizationID     string
	CountryCode        string
	RegistrationNumber string
	LegalName          string
	Address            Address
	AddressType        AddressType // optional at first submission
	Identifiers        map[string]string
	ActorID            string
}

// Submit runs the first verification, or a full resubmission after a failure.
func (s *Service) Submit(ctx context.Context, in *SubmitInput) (*Record, error) {
	in.CountryCode = strings.ToUpper(strings.TrimSpace(in.CountryCode))
	in.RegistrationNumber = strings.TrimSpace(in.RegistrationNumber)
	in.LegalName = strings.TrimSpace(in.LegalName)
	if in.OrganizationID == "" || len(in.CountryCode) != 2 || in.RegistrationNumber == "" || in.LegalName == "" {
		return nil, ErrInvalidInput
	}
	if in.AddressType != "" && !in.AddressType.Valid() {
		return nil, ErrInvalidInput
	}

	rec, err := s.Store.Load(ctx, in.OrganizationID)
	if err != nil {
		return nil, err
	}
	if rec != nil {
		if err := checkResubmittable(rec); err != nil {
			return nil, err
		}
	} else {
		rec = &Record{OrganizationID: in.OrganizationID, Status: StatusNotStarted}
	}

	rec.CountryCode = in.CountryCode
	rec.RegistrationNumber = in.RegistrationNumber
	rec.LegalName = in.LegalName
	rec.Address = in.Address
	rec.AddressType = in.AddressType
	rec.Identifiers = in.Identifiers
	return s.run(ctx, rec, "submitted", in.ActorID)
}

// Resubmit is the light retry form: company name and registration number only
// (KYB V2 §5.3). Country and address are reused from the first submission.
func (s *Service) Resubmit(
	ctx context.Context, organizationID, legalName, registrationNumber, actorID string,
) (*Record, error) {
	legalName, registrationNumber = strings.TrimSpace(legalName), strings.TrimSpace(registrationNumber)
	if organizationID == "" || legalName == "" || registrationNumber == "" {
		return nil, ErrInvalidInput
	}
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != StatusFailed {
		return nil, ErrNothingToRetry
	}
	if rec.FailureReason.HardBlock() {
		return nil, ErrResubmissionBlocked
	}
	if !rec.FailureReason.LightRetry() {
		return nil, ErrActionNotAllowed
	}
	rec.LegalName = legalName
	rec.RegistrationNumber = registrationNumber
	return s.run(ctx, rec, "resubmitted", actorID)
}

// ConfirmName is the one-click "confirm this is us" for a name mismatch: the
// registry's legal name replaces what the user typed and the check runs again,
// so the address check still applies.
func (s *Service) ConfirmName(ctx context.Context, organizationID, actorID string) (*Record, error) {
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != StatusFailed || rec.FailureReason != ReasonNameMismatch || rec.RegistryName == "" {
		return nil, ErrActionNotAllowed
	}
	rec.LegalName = rec.RegistryName
	return s.run(ctx, rec, "name_confirmed", actorID)
}

// ConfirmAddress accepts an address mismatch once the user says which kind of
// address they entered. It is a soft warning, never a block.
func (s *Service) ConfirmAddress(
	ctx context.Context, organizationID string, t AddressType, actorID string,
) (*Record, error) {
	if !t.Valid() {
		return nil, ErrInvalidInput
	}
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != StatusFailed || !rec.FailureReason.SoftWarning() {
		return nil, ErrActionNotAllowed
	}
	now := s.now()
	rec.AddressType = t
	rec.Status, rec.FailureReason = StatusVerified, ""
	rec.VerifiedAt, rec.CheckedAt = &now, &now
	rec.NoticesSent = 0
	ev := Event{OrganizationID: organizationID, Name: "address_confirmed", Status: StatusVerified, ActorID: actorID,
		Detail: map[string]any{"address_type": string(t)}, At: now}
	return rec, s.Store.Save(ctx, rec, ev)
}

// ChangeCountry is the distinct "change country" action: the registration number
// and country-specific identifiers are cleared, and name and address are kept.
// The status returns to not_started so the full country-specific form shows.
func (s *Service) ChangeCountry(ctx context.Context, organizationID, countryCode, actorID string) (*Record, error) {
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if len(countryCode) != 2 {
		return nil, ErrInvalidInput
	}
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != StatusFailed || rec.FailureReason.HardBlock() {
		return nil, ErrActionNotAllowed
	}
	now := s.now()
	from := rec.CountryCode
	rec.CountryCode = countryCode
	rec.RegistrationNumber, rec.Identifiers = "", nil
	rec.Status, rec.FailureReason = StatusNotStarted, ""
	rec.RegistryName, rec.RegisteredAddress = "", ""
	rec.Tier = s.Registry.TierFor(countryCode)
	rec.NoticesSent = 0
	ev := Event{OrganizationID: organizationID, Name: "country_changed", Status: StatusNotStarted, ActorID: actorID,
		Detail: map[string]any{"from": from, "to": countryCode}, At: now}
	return rec, s.Store.Save(ctx, rec, ev)
}

// Review records an operator's decision on a Tier 2 submission. A rejection needs a reason.
func (s *Service) Review(
	ctx context.Context, organizationID, reviewerID string, approve bool, reason FailureReason,
) (*Record, error) {
	if reviewerID == "" || (!approve && !reason.Valid()) {
		return nil, ErrInvalidInput
	}
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Status != StatusPending || rec.Tier != Tier2 {
		return nil, ErrActionNotAllowed
	}
	now := s.now()
	rec.ReviewerID, rec.CheckedAt = reviewerID, &now
	var ev Event
	if approve {
		rec.Status, rec.FailureReason, rec.VerifiedAt = StatusVerified, "", &now
		ev = Event{OrganizationID: organizationID, Name: "review_approved", Status: StatusVerified,
			ActorID: reviewerID, At: now}
	} else {
		rec.Status, rec.FailureReason, rec.NoticesSent = StatusFailed, reason, 0
		ev = Event{OrganizationID: organizationID, Name: "review_rejected", Status: StatusFailed,
			Reason: reason, ActorID: reviewerID, At: now}
	}
	if err := s.Store.Save(ctx, rec, ev); err != nil {
		return nil, err
	}
	if err := s.Queue.Resolve(ctx, organizationID, reviewerID, approve); err != nil {
		return rec, err
	}
	if !approve {
		s.notify(ctx, organizationID, NoticeFailed, reason)
	}
	return rec, nil
}

func checkResubmittable(rec *Record) error {
	switch rec.Status {
	case StatusVerified:
		return ErrAlreadyVerified
	case StatusPending:
		return ErrReviewInProgress
	case StatusFailed:
		if rec.FailureReason.HardBlock() {
			return ErrResubmissionBlocked
		}
	case StatusNotStarted:
	}
	return nil
}

// run verifies rec as currently filled in and persists the outcome.
func (s *Service) run(ctx context.Context, rec *Record, eventName, actorID string) (*Record, error) {
	now := s.now()
	rec.Attempts++
	rec.CheckedAt = &now
	rec.NoticesSent = 0
	rec.Tier = s.Registry.TierFor(rec.CountryCode)

	var res Result
	if s.Formats != nil && !s.Formats(rec.CountryCode, rec.RegistrationNumber) {
		res = Result{Outcome: OutcomeFailed, Reason: ReasonWrongCountry}
	} else {
		var err error
		res, rec.Tier, err = s.Registry.Verify(ctx, &Request{
			CountryCode: rec.CountryCode, RegistrationNumber: rec.RegistrationNumber,
			LegalName: rec.LegalName, Address: rec.Address, Identifiers: rec.Identifiers,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTemporarilyUnavailable, err)
		}
	}
	rec.RegistryName, rec.RegisteredAddress = res.RegistryName, res.RegisteredAddress

	events := []Event{{OrganizationID: rec.OrganizationID, Name: eventName, Status: StatusPending,
		ActorID: actorID, At: now,
		Detail: map[string]any{"country": rec.CountryCode, "tier": rec.Tier, "attempt": rec.Attempts}}}

	var notice Notice
	switch res.Outcome {
	case OutcomeVerified:
		rec.Status, rec.FailureReason, rec.VerifiedAt = StatusVerified, "", &now
		events = append(events, Event{OrganizationID: rec.OrganizationID, Name: "verified", Status: StatusVerified, At: now})
	case OutcomeNeedsReview:
		rec.Status, rec.FailureReason = StatusPending, ""
		events = append(events, Event{OrganizationID: rec.OrganizationID, Name: "queued_for_review",
			Status: StatusPending, At: now})
		notice = NoticeReviewQueue
	case OutcomeFailed:
		if !res.Reason.Valid() {
			return nil, fmt.Errorf("%w: verifier returned an unknown failure reason", ErrTemporarilyUnavailable)
		}
		rec.Status, rec.FailureReason = StatusFailed, res.Reason
		events = append(events, Event{OrganizationID: rec.OrganizationID, Name: "failed",
			Status: StatusFailed, Reason: res.Reason, At: now})
		notice = NoticeFailed
	default:
		return nil, fmt.Errorf("%w: verifier returned an unknown outcome", ErrTemporarilyUnavailable)
	}

	if err := s.Store.Save(ctx, rec, events...); err != nil {
		return nil, err
	}
	if rec.Status == StatusPending {
		if err := s.Queue.Enqueue(ctx, rec.OrganizationID, rec.CountryCode); err != nil {
			return rec, err
		}
	}
	if notice != "" {
		s.notify(ctx, rec.OrganizationID, notice, rec.FailureReason)
	}
	return rec, nil
}

func (s *Service) notify(ctx context.Context, organizationID string, n Notice, reason FailureReason) {
	if s.Notifier == nil {
		return
	}
	// A failed email must never undo or fail a verification, so it is only logged.
	if err := s.Notifier.Notify(ctx, organizationID, n, reason); err != nil {
		s.logger().Warn("kyb: notification failed", "organization_id", organizationID, "notice", string(n), "error", err)
	}
}

// Re-engagement timing from KYB V2 §5.4. The first nudge is at the early end of "+2 to 3 days".
const (
	Nudge1After = 48 * time.Hour
	Nudge2After = 7 * 24 * time.Hour
	DeleteAfter = 30 * 24 * time.Hour
)

// NextNotice returns the next re-engagement email due for a failed verification,
// given how many nudges were already sent. The cadence stops once the user
// resubmits or is verified, so only the failed status produces a notice.
func NextNotice(rec *Record, now time.Time) (Notice, bool) {
	if rec == nil || rec.Status != StatusFailed || rec.CheckedAt == nil {
		return "", false
	}
	age := now.Sub(*rec.CheckedAt)
	switch {
	case rec.NoticesSent == 0 && age >= Nudge1After:
		return NoticeNudge1, true
	case rec.NoticesSent == 1 && age >= Nudge2After:
		return NoticeNudge2, true
	}
	return "", false
}

// DeletionDue reports whether an account left unresolved for 30 days is due for
// deletion (KYB V2 §5.4). Deleting is a separate, reviewed job; this only flags it.
func DeletionDue(rec *Record, now time.Time) bool {
	return rec != nil && rec.Status == StatusFailed && rec.CheckedAt != nil && now.Sub(*rec.CheckedAt) >= DeleteAfter
}
