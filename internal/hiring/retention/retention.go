// Package retention deletes personal data when its retention date has passed. For each company it
//
//  1. shortens the retention date of applications on jobs that have closed, because the clock for a candidate who
//     was never decided starts when the job closes, not when they applied;
//  2. deletes every application whose retention date has passed: first its files in object storage, then, in one
//     transaction, the application with its encrypted special category answers, review task and unsent emails,
//     leaving an entry in the append-only compliance log;
//  3. deletes expired reapply blocks and old outbox rows;
//  4. queues one email to the company saying how many applications were deleted for which job.
//
// The log entry and the email carry the application id and counts only, never anything about the candidate.
// A failure on one application is counted and the run goes on; the application is tried again next run.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Angle-HR/server/internal/hiring/uploads"
)

// Due is an application whose retention date has passed.
type Due struct {
	ID       string
	JobID    string
	JobCode  string
	JobTitle string
	FileIDs  []string
}

// NoticeItem is one line of the email to the company.
type NoticeItem struct {
	JobCode  string `json:"job_code"`
	JobTitle string `json:"job_title"`
	Count    int    `json:"count"`
}

// Store is one region's database as the retention task uses it.
type Store interface {
	// ShortenForClosedJobs pulls the retention date of undecided applications on closed or expired jobs back to
	// the job's close date plus its retention months. It never extends a date.
	ShortenForClosedJobs(ctx context.Context, tenantID string, now time.Time) (int, error)
	DueApplications(ctx context.Context, tenantID string, now time.Time, limit int) ([]Due, error)
	// DeleteApplication deletes one due application and everything that belongs to it, and writes the
	// compliance log entry, in one transaction. It does nothing when the application is no longer due.
	DeleteApplication(ctx context.Context, tenantID string, d Due, now time.Time) error
	DeleteExpiredBlocks(ctx context.Context, tenantID string, now time.Time) (int, error)
	DeleteOldEmails(ctx context.Context, tenantID string, now time.Time) (int, error)
	// QueueNotice saves the email to the company's privacy contact (or its owner).
	QueueNotice(ctx context.Context, tenantID string, now time.Time, items []NoticeItem) error
}

// Storage is object storage as the retention task uses it.
type Storage interface {
	Remove(ctx context.Context, key string) error
}

// Sweeper runs the retention task over a region.
type Sweeper struct {
	Tenants func(ctx context.Context) ([]string, error)
	Store   Store
	Storage Storage
	Now     func() time.Time
	Logger  *slog.Logger
}

// Result counts what a run did.
type Result struct {
	Companies, Shortened, Deleted, Blocks, Emails, Errors int
}

const batch = 100

// Run goes through every company once.
func (w *Sweeper) Run(ctx context.Context) (Result, error) {
	var res Result
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	tenants, err := w.Tenants(ctx)
	if err != nil {
		return res, fmt.Errorf("retention: list companies: %w", err)
	}
	for _, tid := range tenants {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Companies++
		w.company(ctx, tid, now, &res)
	}
	if res.Errors > 0 {
		return res, errors.New("retention: some work failed and will be retried")
	}
	return res, nil
}

func (w *Sweeper) company(ctx context.Context, tid string, now time.Time, res *Result) {
	fail := func(what string, err error) {
		res.Errors++
		if w.Logger != nil {
			w.Logger.Error("retention: "+what, "error", err)
		}
	}
	n, err := w.Store.ShortenForClosedJobs(ctx, tid, now)
	if err != nil {
		fail("shorten retention for closed jobs", err)
	}
	res.Shortened += n

	perJob := map[string]*NoticeItem{}
	for {
		list, err := w.Store.DueApplications(ctx, tid, now, batch)
		if err != nil {
			fail("list due applications", err)
			break
		}
		failed := 0
		for _, d := range list {
			if err := w.delete(ctx, tid, d, now); err != nil {
				failed++
				fail("delete application "+d.ID, err)
				continue
			}
			res.Deleted++
			it := perJob[d.JobID]
			if it == nil {
				it = &NoticeItem{JobCode: d.JobCode, JobTitle: d.JobTitle}
				perJob[d.JobID] = it
			}
			it.Count++
		}
		// Stop on a short page, or when nothing in the page could be deleted (it would come back forever).
		if len(list) < batch || failed == len(list) || ctx.Err() != nil {
			break
		}
	}

	if n, err = w.Store.DeleteExpiredBlocks(ctx, tid, now); err != nil {
		fail("delete expired reapply blocks", err)
	}
	res.Blocks += n
	if _, err = w.Store.DeleteOldEmails(ctx, tid, now); err != nil {
		fail("delete old outbox emails", err)
	}

	if len(perJob) > 0 {
		items := make([]NoticeItem, 0, len(perJob))
		for _, it := range perJob {
			items = append(items, *it)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].JobCode < items[j].JobCode })
		if err := w.Store.QueueNotice(ctx, tid, now, items); err != nil {
			fail("queue notice to the company", err)
			return
		}
		res.Emails++
	}
}

// delete removes the files first. A file that is already gone is fine; a file that cannot be removed stops the
// deletion of this application, so no row ever outlives the pointer needed to find its object.
func (w *Sweeper) delete(ctx context.Context, tid string, d Due, now time.Time) error {
	for _, id := range d.FileIDs {
		for _, key := range []string{uploads.StagingKey(tid, d.JobID, id), uploads.FinalKey(tid, d.JobID, id)} {
			if err := w.Storage.Remove(ctx, key); err != nil {
				return fmt.Errorf("remove file %s: %w", id, err)
			}
		}
	}
	return w.Store.DeleteApplication(ctx, tid, d, now)
}
