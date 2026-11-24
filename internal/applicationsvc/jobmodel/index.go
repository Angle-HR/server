package jobmodel

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
)

// IndexWriter records which region and company hold a public job id.
type IndexWriter interface {
	PutJob(ctx context.Context, publicID, region, companyID, jobID string, version int64) error
}

// IndexReader resolves a public job id; apperror.ErrNotFound for an id it has never seen.
type IndexReader interface {
	GetJob(ctx context.Context, publicID string) (appjobs.Ref, error)
}

// PGIndex is the index in the global database: hiring.app_job_index (db/migrations/global_registry). DB is the
// global pool (*pgxpool.Pool).
type PGIndex struct {
	DB interface {
		Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

const putIndexSQL = `
INSERT INTO hiring.app_job_index (public_id, region, company_id, job_id, version)
VALUES ($1, $2, $3::uuid, $4::uuid, $5)
ON CONFLICT (public_id) DO UPDATE SET region = EXCLUDED.region, company_id = EXCLUDED.company_id,
    job_id = EXCLUDED.job_id, version = EXCLUDED.version, updated_at = now()
WHERE hiring.app_job_index.version < EXCLUDED.version`

// PutJob implements IndexWriter. A lower or equal version leaves the row alone.
func (x *PGIndex) PutJob(ctx context.Context, publicID, region, companyID, jobID string, version int64) error {
	_, err := x.DB.Exec(ctx, putIndexSQL, publicID, region, companyID, jobID, version)
	return err
}

const getIndexSQL = `SELECT region, company_id::text FROM hiring.app_job_index WHERE public_id = $1`

// GetJob implements IndexReader.
func (x *PGIndex) GetJob(ctx context.Context, publicID string) (appjobs.Ref, error) {
	ref := appjobs.Ref{PublicID: publicID}
	err := x.DB.QueryRow(ctx, getIndexSQL, publicID).Scan(&ref.Region, &ref.OrganizationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return appjobs.Ref{}, apperror.ErrNotFound
	}
	if err != nil {
		return appjobs.Ref{}, fmt.Errorf("jobmodel: index lookup: %w", err)
	}
	return ref, nil
}

// Stats counts how often the model had to ask its Fallback (hiring tables), by method and reason. With events on
// and the read model caught up the counters of JobAccess, TenantIDs, LookupJob and NoticeRecipient stay at zero;
// docs/microservices/applications-jobmodel.md says what each reason means.
type Stats struct {
	mu sync.Mutex
	n  map[string]int64
	// OnFallback, when set, is called for every fallback use (for a log line).
	OnFallback func(method, reason string)
}

func (s *Stats) hit(method, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.n == nil {
		s.n = map[string]int64{}
	}
	s.n[method+":"+reason]++
	s.mu.Unlock()
	if s.OnFallback != nil {
		s.OnFallback(method, reason)
	}
}

// Snapshot returns a copy of the counters, keyed "method:reason", plus the total under "total".
func (s *Stats) Snapshot() map[string]int64 {
	out := map[string]int64{"total": 0}
	if s == nil {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.n))
	for k := range s.n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out[k] = s.n[k]
		out["total"] += s.n[k]
	}
	return out
}
