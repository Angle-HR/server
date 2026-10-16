package hiringstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/uploads"
)

const insertUploadSQL = `
INSERT INTO hiring.application_files
    (id, tenant_id, job_id, question_id, original_filename, content_type, declared_size_bytes, status, expires_at,
     retention_until, lawful_basis, collection_purpose, allowed_downstream_purposes)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, 'pending', $8, $9, $10, $11, '{}')`

// InsertUpload records an upload URL that was issued. It implements uploads.Store.
func (s *Store) InsertUpload(ctx context.Context, tenantID string, r *uploads.Record) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertUploadSQL, r.ID, tenantID, r.JobID, r.QuestionID, r.Filename, r.ContentType,
			r.Declared, r.ExpiresAt, r.RetentionUntil, r.LawfulBasis, r.CollectionPurpose)
		if err != nil {
			return fmt.Errorf("hiringstore: insert upload: %w", mapPgError(err))
		}
		return nil
	})
}

const getUploadSQL = `
SELECT id::text, tenant_id::text, job_id::text, question_id, original_filename, content_type, declared_size_bytes,
       COALESCE(size_bytes, 0), COALESCE(sha256, ''), status, expires_at
FROM hiring.application_files
WHERE id = $1::uuid AND tenant_id = $2::uuid`

// GetUpload reads one upload of the company. It implements uploads.Store.
func (s *Store) GetUpload(ctx context.Context, tenantID, id string) (*uploads.Record, error) {
	var out *uploads.Record
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		r := &uploads.Record{}
		err := tx.QueryRow(ctx, getUploadSQL, id, tenantID).Scan(&r.ID, &r.TenantID, &r.JobID, &r.QuestionID,
			&r.Filename, &r.ContentType, &r.Declared, &r.Size, &r.SHA256, &r.Status, &r.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("hiringstore: load upload: %w", err)
		}
		out = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Only a pending upload can become clean, and only a pending or clean one can be rejected: a claimed file is
// never touched by a late claim.
const markUploadCleanSQL = `
UPDATE hiring.application_files SET status = 'clean', size_bytes = $3, sha256 = $4
WHERE id = $1::uuid AND tenant_id = $2::uuid AND status IN ('pending', 'clean') AND application_id IS NULL`

// MarkUploadClean records that an upload passed every check. It implements uploads.Store.
func (s *Store) MarkUploadClean(ctx context.Context, tenantID, id string, size int64, sha string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, markUploadCleanSQL, id, tenantID, size, sha)
		if err != nil {
			return fmt.Errorf("hiringstore: mark upload clean: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
}

// The filename is blanked: a rejected upload keeps nothing a person typed or chose.
const rejectUploadSQL = `
UPDATE hiring.application_files SET status = 'rejected', original_filename = ''
WHERE id = $1::uuid AND tenant_id = $2::uuid AND status IN ('pending', 'clean') AND application_id IS NULL`

// RejectUpload marks an upload as failed. It implements uploads.Store.
func (s *Store) RejectUpload(ctx context.Context, tenantID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, rejectUploadSQL, id, tenantID); err != nil {
			return fmt.Errorf("hiringstore: reject upload: %w", err)
		}
		return nil
	})
}

const expiredUploadsSQL = `
SELECT id::text, job_id::text
FROM hiring.application_files
WHERE tenant_id = $1::uuid AND expires_at IS NOT NULL AND expires_at < $2 AND application_id IS NULL
ORDER BY expires_at
LIMIT $3`

// ExpiredUploads lists unclaimed uploads that ran out of time. It implements uploads.SweepStore.
func (s *Store) ExpiredUploads(ctx context.Context, tenantID string, now time.Time, limit int) ([]uploads.Expired, error) {
	var out []uploads.Expired
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, expiredUploadsSQL, tenantID, now, limit)
		if err != nil {
			return fmt.Errorf("hiringstore: list expired uploads: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			e := uploads.Expired{TenantID: tenantID}
			if err = rows.Scan(&e.ID, &e.JobID); err != nil {
				return fmt.Errorf("hiringstore: scan expired upload: %w", err)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

const deleteUploadSQL = `
DELETE FROM hiring.application_files
WHERE id = $1::uuid AND tenant_id = $2::uuid AND application_id IS NULL`

// DeleteUpload removes the row of an unclaimed upload. It implements uploads.SweepStore.
func (s *Store) DeleteUpload(ctx context.Context, tenantID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, deleteUploadSQL, id, tenantID); err != nil {
			return fmt.Errorf("hiringstore: delete upload: %w", err)
		}
		return nil
	})
}

// A file belongs to an application only if it is clean, unowned, on the same job and for the same question.
// The application's retention date replaces the pending upload's.
const attachFileSQL = `
UPDATE hiring.application_files
SET status = 'claimed', application_id = $1::uuid, expires_at = NULL, retention_until = $2
WHERE id = $3::uuid AND tenant_id = $4::uuid AND job_id = $5::uuid AND question_id = $6
  AND status = 'clean' AND application_id IS NULL AND expires_at > now()`
