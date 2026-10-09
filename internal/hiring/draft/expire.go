package draft

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/lifecycle"
)

// ExpireDue moves the company's published jobs whose closing date has passed to expired. It is the system
// action behind lifecycle.ActionExpire, so no person is recorded as the actor; the audit row says why. It
// returns how many jobs were expired. A job that changed since it was found (closed, paused or reopened by
// someone) is skipped, not an error.
func (s *Service) ExpireDue(ctx context.Context, tenantID string, limit int) (int, error) {
	ids, err := s.Store.DueJobIDs(ctx, tenantID, limit)
	if err != nil {
		return 0, fmt.Errorf("draft: find due jobs: %w", err)
	}
	expired := 0
	var firstErr error
	for _, id := range ids {
		ok, err := s.expireOne(ctx, tenantID, id)
		if err != nil {
			slog.ErrorContext(ctx, "draft: expire job", "job", id, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if ok {
			expired++
		}
	}
	return expired, firstErr
}

func (s *Service) expireOne(ctx context.Context, tenantID, id string) (bool, error) {
	var publicID string
	skipped := false
	_, err := s.Store.UpdateJob(ctx, tenantID, "", id, 0,
		func(cur *hiringtypes.JobRecord) (*hiringtypes.UpdateResult, error) {
			// Decide again under the row lock: the job may have moved, or its closing date may have changed.
			p, perr := lifecycle.Next(cur.Job.Status, lifecycle.ActionExpire)
			if perr != nil || cur.Job.ClosingDate == "" || cur.Job.ClosingDate >= s.now().UTC().Format("2006-01-02") {
				skipped = true
				return nil, nil
			}
			publicID = cur.Job.PublicID
			return &hiringtypes.UpdateResult{
				Completed: cur.Job.CompletedSections, Step: cur.Job.CurrentStep,
				Status:      &hiringtypes.StatusChange{To: p.To},
				AuditAction: "job.expire",
				AuditDiff:   map[string]any{"from": cur.Job.Status, "to": p.To, "reason": "closing_date_passed"},
			}, nil
		})
	if err != nil {
		return false, err
	}
	if skipped {
		return false, nil
	}
	if publicID != "" {
		if rerr := s.Ref.SetRegistryStatus(ctx, publicID, registryStatus(jobs.StatusExpired)); rerr != nil {
			slog.WarnContext(ctx, "draft: registry status not updated", "job", id, "error", rerr)
		}
	}
	return true, nil
}
