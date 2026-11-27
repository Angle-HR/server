package appstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/outbox"
)

var _ applications.AttachmentStore = (*Store)(nil)

// AddAttachment implements applications.AttachmentStore.
func (s *Store) AddAttachment(ctx context.Context, tenantID, jobID, appID, actorID string, a *applications.Attachment) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		var waiting int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM hiring.app_message_attachments WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND message_id IS NULL`,
			tenantID, appID).Scan(&waiting); err != nil {
			return fmt.Errorf("appstore: count attachments: %w", err)
		}
		if waiting >= applications.MaxUnsentAttachments {
			return applications.ErrTooManyAttachments
		}
		_, err := tx.Exec(ctx, `INSERT INTO hiring.app_message_attachments (id, tenant_id, job_id, application_id, uploaded_by, filename, content_type, size, sha256, object_key)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid, $6, $7, $8, $9, $10)`,
			a.ID, tenantID, jobID, appID, actorID, a.Filename, a.ContentType, a.Size, a.SHA256, a.ObjectKey)
		if err != nil {
			return fmt.Errorf("appstore: save attachment: %w", err)
		}
		return nil
	})
}

// UnsentAttachments implements applications.AttachmentStore.
func (s *Store) UnsentAttachments(ctx context.Context, tenantID, jobID, appID string) ([]applications.Attachment, error) {
	out := []applications.Attachment{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, filename, content_type, size FROM hiring.app_message_attachments
WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND message_id IS NULL ORDER BY created_at`, tenantID, appID)
		if err != nil {
			return fmt.Errorf("appstore: list attachments: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var a applications.Attachment
			if err = rows.Scan(&a.ID, &a.Filename, &a.ContentType, &a.Size); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteAttachment implements applications.AttachmentStore.
func (s *Store) DeleteAttachment(ctx context.Context, tenantID, jobID, appID, id string) (string, error) {
	var key string
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `DELETE FROM hiring.app_message_attachments WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid
AND application_id = $4::uuid AND message_id IS NULL RETURNING object_key`, id, tenantID, jobID, appID).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return key, err
}

// linkAttachments attaches uploaded files to a message in tx. Every id must be an unsent file of the same
// application, or nothing is written.
func linkAttachments(ctx context.Context, tx pgx.Tx, tenantID, appID, messageID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `UPDATE hiring.app_message_attachments SET message_id = $4::uuid
WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND message_id IS NULL AND id = ANY($3::uuid[])`, tenantID, appID, ids, messageID)
	if err != nil {
		return fmt.Errorf("appstore: attach files: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		return applications.ErrUnknownAttachment
	}
	return nil
}

// attachmentsOf lists the files of the given messages, by message id.
func attachmentsOf(ctx context.Context, tx pgx.Tx, tenantID string, messageIDs []string) (map[string][]applications.Attachment, error) {
	out := map[string][]applications.Attachment{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT message_id::text, id::text, filename, content_type, size FROM hiring.app_message_attachments
WHERE tenant_id = $1::uuid AND message_id = ANY($2::uuid[]) ORDER BY created_at, id`, tenantID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("appstore: message attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var a applications.Attachment
		if err = rows.Scan(&mid, &a.ID, &a.Filename, &a.ContentType, &a.Size); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], a)
	}
	return out, rows.Err()
}

// dueAttachments lists the files to send with the leased messages, by message id.
func dueAttachments(ctx context.Context, tx pgx.Tx, tenantID string, messageIDs []string) (map[string][]outbox.DueAttachment, error) {
	out := map[string][]outbox.DueAttachment{}
	if len(messageIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT message_id::text, object_key, filename, content_type FROM hiring.app_message_attachments
WHERE tenant_id = $1::uuid AND message_id = ANY($2::uuid[]) ORDER BY created_at, id`, tenantID, messageIDs)
	if err != nil {
		return nil, fmt.Errorf("appstore: due attachments: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mid string
		var a outbox.DueAttachment
		if err = rows.Scan(&mid, &a.Key, &a.Filename, &a.ContentType); err != nil {
			return nil, err
		}
		out[mid] = append(out[mid], a)
	}
	return out, rows.Err()
}
