package uploads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Expired is an upload nobody used before it expired.
type Expired struct {
	ID       string
	JobID    string
	TenantID string
}

// SweepStore is what the sweeper needs from a region's database.
type SweepStore interface {
	// ExpiredUploads lists up to limit unclaimed uploads whose time is up.
	ExpiredUploads(ctx context.Context, tenantID string, now time.Time, limit int) ([]Expired, error)
	// DeleteUpload deletes the row of an unclaimed upload.
	DeleteUpload(ctx context.Context, tenantID, id string) error
}

// Sweeper deletes uploads no application used: the object under both keys, then the row. It works one company
// at a time because row security hides every row until a company is chosen.
type Sweeper struct {
	Tenants func(ctx context.Context) ([]string, error)
	Store   SweepStore
	Storage Storage
	Now     func() time.Time
	Logger  *slog.Logger
}

// SweepResult counts what a run did.
type SweepResult struct{ Companies, Deleted, Errors int }

const sweepBatch = 200

// Run sweeps every company once. A failure on one upload or company is counted and the run continues.
func (w *Sweeper) Run(ctx context.Context) (SweepResult, error) {
	var res SweepResult
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	tenants, err := w.Tenants(ctx)
	if err != nil {
		return res, fmt.Errorf("uploads: list companies: %w", err)
	}
	for _, tid := range tenants {
		res.Companies++
		for {
			list, err := w.Store.ExpiredUploads(ctx, tid, now, sweepBatch)
			if err != nil {
				res.Errors++
				w.log("list expired uploads", err)
				break
			}
			failed := 0
			for _, u := range list {
				if err := w.delete(ctx, tid, u); err != nil {
					failed++
					res.Errors++
					w.log("delete upload", err)
					continue
				}
				res.Deleted++
			}
			// Stop on a short page, or when nothing in the page could be deleted (it would come back forever).
			if len(list) < sweepBatch || failed == len(list) {
				break
			}
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
		}
	}
	if res.Errors > 0 {
		return res, errors.New("uploads: some uploads could not be deleted")
	}
	return res, nil
}

func (w *Sweeper) delete(ctx context.Context, tenantID string, u Expired) error {
	// Objects first: a row without an object is harmless, an object without a row would never be found.
	for _, key := range []string{StagingKey(tenantID, u.JobID, u.ID), FinalKey(tenantID, u.JobID, u.ID)} {
		if err := w.Storage.Remove(ctx, key); err != nil {
			return fmt.Errorf("remove %s: %w", u.ID, err)
		}
	}
	return w.Store.DeleteUpload(ctx, tenantID, u.ID)
}

func (w *Sweeper) log(msg string, err error) {
	if w.Logger != nil {
		w.Logger.Error("uploads sweep: "+msg, "error", err)
	}
}
