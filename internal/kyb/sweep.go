package kyb

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// SweepStore is what the re-engagement sweep needs from one region's database.
type SweepStore interface {
	Store
	// DueOrganizations lists organizations whose verification failed at or before the given time.
	DueOrganizations(ctx context.Context, failedBefore time.Time) ([]string, error)
	// RecordNotice counts one more nudge and records it in the event history.
	RecordNotice(ctx context.Context, organizationID, notice string, at time.Time) error
	// FlagDeletion marks the account for a reviewed deletion job. It reports whether the flag is new.
	FlagDeletion(ctx context.Context, organizationID string, at time.Time) (bool, error)
}

// Sweeper sends the re-engagement emails of KYB V2 §5.4 and flags accounts that
// stayed unresolved for 30 days. It never deletes anything.
type Sweeper struct {
	Store    SweepStore
	Notifier Notifier
	Logger   *slog.Logger // optional
	Now      func() time.Time
}

// SweepResult counts what one sweep did.
type SweepResult struct {
	Nudged  int
	Flagged int
	Errors  int
}

func (s *Sweeper) logger() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Run handles every organization that is due. An error on one organization is
// logged and counted, and the sweep carries on with the rest.
func (s *Sweeper) Run(ctx context.Context) (SweepResult, error) {
	now := s.now()
	ids, err := s.Store.DueOrganizations(ctx, now.Add(-Nudge1After))
	if err != nil {
		return SweepResult{}, fmt.Errorf("kyb: sweep: %w", err)
	}
	var res SweepResult
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if err := s.handle(ctx, id, now, &res); err != nil {
			res.Errors++
			s.logger().Warn("kyb: sweep failed for organization", "organization_id", id, "error", err)
		}
	}
	return res, nil
}

func (s *Sweeper) handle(ctx context.Context, id string, now time.Time, res *SweepResult) error {
	rec, err := s.Store.Load(ctx, id)
	if err != nil {
		return err
	}
	if rec == nil {
		return nil
	}
	if DeletionDue(rec, now) {
		flagged, err := s.Store.FlagDeletion(ctx, id, now)
		if err != nil {
			return err
		}
		if flagged {
			res.Flagged++
		}
	}
	notice, due := NextNotice(rec, now)
	if !due {
		return nil
	}
	// The email goes first: if it fails nothing is counted, so the next sweep retries it.
	if s.Notifier != nil {
		if err := s.Notifier.Notify(ctx, id, notice, rec.FailureReason); err != nil {
			return err
		}
	}
	if err := s.Store.RecordNotice(ctx, id, string(notice), now); err != nil {
		return err
	}
	res.Nudged++
	return nil
}
