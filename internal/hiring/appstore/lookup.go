package appstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const jobOfApplicationSQL = `SELECT job_id::text FROM hiring.applications WHERE id = $1::uuid AND tenant_id = $2::uuid`

// JobOfApplication returns the job an application belongs to, for callers (the gRPC API) that address an
// application by its id alone. ErrNotFound when the company has no such application.
func (s *Store) JobOfApplication(ctx context.Context, tenantID, applicationID string) (string, error) {
	var jobID string
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, jobOfApplicationSQL, applicationID, tenantID).Scan(&jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: job of application: %w", err)
		}
		return nil
	})
	return jobID, err
}

// ApplicantCount is the per-job counters the job list shows.
type ApplicantCount struct {
	JobID string
	Total int
	New   int // applications nobody has opened yet
}

const applicantCountsSQL = `
SELECT job_id::text, applicant_count::int, new_applicant_count::int
FROM hiring.job_applicant_counts WHERE tenant_id = $1::uuid AND job_id = ANY($2::uuid[])`

// ApplicantCounts reads the counters of the given jobs. A job with no applications has no row and is left out.
func (s *Store) ApplicantCounts(ctx context.Context, tenantID string, jobIDs []string) ([]ApplicantCount, error) {
	var out []ApplicantCount
	if len(jobIDs) == 0 {
		return out, nil
	}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, applicantCountsSQL, tenantID, jobIDs)
		if err != nil {
			return fmt.Errorf("appstore: applicant counts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c ApplicantCount
			if err = rows.Scan(&c.JobID, &c.Total, &c.New); err != nil {
				return fmt.Errorf("appstore: scan applicant counts: %w", err)
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
