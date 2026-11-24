package hiringstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/draft"
)

// auditEventsSQL merges the two audit tables of the shared hiring schema: hiring's own rows and the applications
// service's (hiring.app_audit_events, migration 000021). Rows written before 000021 stay in hiring.audit_events.
// Newest first by (created_at, source, id); the cursor is the last row of the previous page.
const auditEventsSQL = `
SELECT source, id, job_id::text, actor_id::text, action, diff, created_at FROM (
    SELECT 'hiring' AS source, id, job_id, actor_id, action, diff, created_at
      FROM hiring.audit_events WHERE tenant_id = $1::uuid
    UNION ALL
    SELECT 'applications', id, job_id, actor_id, action, diff, created_at
      FROM hiring.app_audit_events WHERE tenant_id = $1::uuid
) e
WHERE ($2::text = '' OR job_id = $2::uuid)
  AND ($3::text = '' OR actor_id = $3::uuid)
  AND ($4::text = '' OR action = $4)
  AND ($5::timestamptz IS NULL OR created_at >= $5)
  AND ($6::timestamptz IS NULL OR created_at < $6)
  AND ($7::timestamptz IS NULL OR (created_at, source, id) < ($7, $8::text, $9::bigint))
ORDER BY created_at DESC, source DESC, id DESC
LIMIT $10`

// AuditEvents implements draft.AuditReader. The read runs in the company's tenant transaction, so row level
// security applies on top of the tenant_id predicate.
func (s *Store) AuditEvents(ctx context.Context, tenantID string, f draft.AuditFilter) ([]draft.AuditEvent, error) {
	var (
		cAt  any
		cSrc string
		cID  int64
	)
	if f.After != nil {
		cAt, cSrc, cID = f.After.CreatedAt, f.After.Source, f.After.ID
	}
	out := []draft.AuditEvent{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, auditEventsSQL, tenantID, f.JobID, f.ActorID, f.Action, f.From, f.To, cAt, cSrc, cID, f.Limit)
		if err != nil {
			return fmt.Errorf("hiringstore: audit events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var e draft.AuditEvent
			if err = rows.Scan(&e.Source, &e.ID, &e.JobID, &e.ActorID, &e.Action, &e.Diff, &e.CreatedAt); err != nil {
				return fmt.Errorf("hiringstore: scan audit event: %w", err)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

var _ draft.AuditReader = (*Store)(nil)
