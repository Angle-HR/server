package jobmodel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/pkg/apperror"
)

// JobRef names a job to fill into the model.
type JobRef struct{ TenantID, PublicID string }

// Lister enumerates the jobs of a region that the model should hold. Nothing in the appjobs interfaces can list
// jobs, so the caller (the rebuild command, which runs where it may read hiring) supplies it.
type Lister interface {
	Jobs(ctx context.Context, region string) ([]JobRef, error)
}

// Report counts what Rebuild did in one region.
type Report struct {
	Region  string
	Listed  int // jobs the Lister returned
	Written int // jobs read from the Fallback and handed to the model
	Skipped int // jobs the Fallback did not find or that never published (no frozen form)
	Failed  int
}

// Rebuild fills the model of each region by reading every listed job through the Fallback (LoadForApply) and
// writing it with Remember. It is idempotent and safe next to the projector: rows are written with version 0, so
// they never replace a row an event wrote, and the next event replaces them. A job that fails is logged and
// counted; the run continues and returns an error at the end.
//
// The rows lack the company's privacy contact (the Fallback's apply view does not carry it); running
// cmd/hiring-jobs-backfill instead, which re-emits full snapshots, fills everything. Use Rebuild when events
// cannot be replayed.
func (m *Model) Rebuild(ctx context.Context, log *slog.Logger, l Lister, regions []string) ([]Report, error) {
	if m.Fallback == nil {
		return nil, ErrNoFallback
	}
	var reports []Report
	failed := 0
	for _, reg := range regions {
		rep := Report{Region: reg}
		refs, err := l.Jobs(ctx, reg)
		if err != nil {
			return reports, fmt.Errorf("jobmodel: list jobs of %s: %w", reg, err)
		}
		rep.Listed = len(refs)
		fbRegional, err := m.Fallback.Regional(reg)
		if err != nil {
			return reports, fmt.Errorf("jobmodel: fallback for %s: %w", reg, err)
		}
		local, err := m.Regional(reg)
		if err != nil {
			return reports, fmt.Errorf("jobmodel: model for %s: %w", reg, err)
		}
		for _, ref := range refs {
			t, err := fbRegional.LoadForApply(ctx, ref.TenantID, ref.PublicID)
			if errors.Is(err, apperror.ErrNotFound) {
				rep.Skipped++
				continue
			}
			if err == nil {
				err = local.(*Regional).Remember(ctx, ref.TenantID, ref.PublicID, t)
			}
			if err != nil {
				rep.Failed++
				failed++
				log.Error("jobmodel rebuild: job failed", "region", reg, "company", ref.TenantID, "public_id", ref.PublicID, "error", err)
				continue
			}
			rep.Written++
		}
		reports = append(reports, rep)
	}
	if failed > 0 {
		return reports, fmt.Errorf("jobmodel: rebuild: %d jobs failed", failed)
	}
	return reports, nil
}
