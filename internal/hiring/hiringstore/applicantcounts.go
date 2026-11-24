package hiringstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ApplicantCountRow is one row of hiring.job_applicant_counts, a table the applications side owns and writes.
type ApplicantCountRow struct {
	JobID string
	Total int
	New   int
}

const applicantCountsSQL = `
SELECT job_id::text, applicant_count::int, new_applicant_count::int
FROM hiring.job_applicant_counts WHERE tenant_id = $1::uuid AND job_id = ANY($2::uuid[])`

// ApplicantCounts reads the counters of the given jobs straight from the shared table. It is the applicantcounts.DB
// implementation (APPLICANT_COUNTS=db); the gRPC implementation replaces it once the table is applications' alone.
func (s *Store) ApplicantCounts(ctx context.Context, tenantID string, jobIDs []string) ([]ApplicantCountRow, error) {
	var out []ApplicantCountRow
	if len(jobIDs) == 0 {
		return out, nil
	}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, applicantCountsSQL, tenantID, jobIDs)
		if err != nil {
			return fmt.Errorf("hiringstore: applicant counts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r ApplicantCountRow
			if err = rows.Scan(&r.JobID, &r.Total, &r.New); err != nil {
				return fmt.Errorf("hiringstore: scan applicant counts: %w", err)
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
