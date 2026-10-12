package kyb

import (
	"context"
	"errors"
)

// ErrNotVerified means the organization cannot publish jobs until its company is verified.
var ErrNotVerified = errors.New("kyb: company must be verified before publishing")

// RequirePublish is the publish gate (KYB V2 §5.1): only a verified company may
// publish a job. Drafting is never gated, so there is no equivalent for it.
//
// Hook point: call this from the job-publish handler once job creation exists.
func RequirePublish(s Status) error {
	if !s.CanPublish() {
		return ErrNotVerified
	}
	return nil
}

// RequirePublishFor loads the organization's status and applies RequirePublish.
// An organization with no verification record is not_started.
func RequirePublishFor(ctx context.Context, store Store, organizationID string) error {
	rec, err := store.Load(ctx, organizationID)
	if err != nil {
		return err
	}
	if rec == nil {
		return RequirePublish(StatusNotStarted)
	}
	return RequirePublish(rec.Status)
}

// OwnershipTransferred re-verifies an organization after its owner changes: the
// new owner's company must be verified again. A verified or failed record goes
// back to not_started with the earlier details kept for pre-filling, and the
// change is recorded. A record already pending review is left alone, because a
// person is about to check it anyway. An organization that never started
// verification needs nothing.
//
// Hook point: call this from the ownership-transfer code once it exists, after
// the new owner is saved. newOwnerID is only recorded in the event.
func (s *Service) OwnershipTransferred(
	ctx context.Context, organizationID, newOwnerID, actorID string,
) (*Record, error) {
	if organizationID == "" || newOwnerID == "" {
		return nil, ErrInvalidInput
	}
	rec, err := s.Store.Load(ctx, organizationID)
	if err != nil || rec == nil {
		return nil, err
	}
	if rec.Status != StatusVerified && rec.Status != StatusFailed {
		return rec, nil
	}
	now := s.now()
	from := rec.Status
	rec.Status, rec.FailureReason = StatusNotStarted, ""
	rec.VerifiedAt, rec.ReviewerID = nil, ""
	rec.NoticesSent = 0
	ev := Event{OrganizationID: organizationID, Name: "ownership_transferred", Status: StatusNotStarted,
		ActorID: actorID, At: now,
		Detail: map[string]any{"from": string(from), "new_owner_id": newOwnerID}}
	if err := s.Store.Save(ctx, rec, ev); err != nil {
		return nil, err
	}
	return rec, nil
}
