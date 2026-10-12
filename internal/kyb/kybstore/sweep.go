package kybstore

import (
	"context"
	"fmt"
	"time"

	"github.com/Angle-HR/server/pkg/besteffort"
)

const dueSQL = `
SELECT v.organization_id::text
FROM accounts.organization_verifications v
JOIN accounts.organizations o ON o.id = v.organization_id
WHERE o.kyb_status = 'failed' AND v.checked_at IS NOT NULL AND v.checked_at <= $1
ORDER BY v.checked_at`

// DueOrganizations lists organizations whose verification failed at or before the
// given time. The caller decides which notice, if any, each one is due.
func (s *Store) DueOrganizations(ctx context.Context, failedBefore time.Time) ([]string, error) {
	rows, err := s.DB.Query(ctx, dueSQL, failedBefore)
	if err != nil {
		return nil, fmt.Errorf("kybstore: list due: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("kybstore: scan due: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kybstore: list due: %w", err)
	}
	return ids, nil
}

const noticeSQL = `
UPDATE accounts.organization_verifications SET notices_sent = notices_sent + 1 WHERE organization_id = $1`

// RecordNotice counts one more re-engagement nudge and records it in the event history.
func (s *Store) RecordNotice(ctx context.Context, organizationID, notice string, at time.Time) (err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("kybstore: begin: %w", err)
	}
	defer func() {
		if err != nil {
			besteffort.Log(ctx, "kybstore.rollback", tx.Rollback(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, noticeSQL, organizationID); err != nil {
		return fmt.Errorf("kybstore: count notice: %w", err)
	}
	if _, err = tx.Exec(ctx, eventSQL, organizationID, "notice_sent", "failed", "", "",
		fmt.Appendf(nil, `{"notice":%q}`, notice), at); err != nil {
		return fmt.Errorf("kybstore: save notice event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("kybstore: commit: %w", err)
	}
	return nil
}

const flagSQL = `
UPDATE accounts.organization_verifications SET deletion_flagged_at = $2
WHERE organization_id = $1 AND deletion_flagged_at IS NULL`

// FlagDeletion marks an account that stayed unresolved for 30 days so a reviewed
// deletion job can pick it up. It reports whether the flag was newly set, and it deletes nothing.
func (s *Store) FlagDeletion(ctx context.Context, organizationID string, at time.Time) (bool, error) {
	tag, err := s.DB.Exec(ctx, flagSQL, organizationID, at)
	if err != nil {
		return false, fmt.Errorf("kybstore: flag deletion: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
