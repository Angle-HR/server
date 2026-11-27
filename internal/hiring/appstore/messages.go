package appstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/outbox"
)

var (
	_ applications.MessageStore = (*Store)(nil)
	_ outbox.MessageStore       = (*Store)(nil)
)

// ---- templates ----

const templateCols = `id::text, name, subject, body, updated_at`

func scanTemplate(row pgx.Row) (*applications.Template, error) {
	var t applications.Template
	if err := row.Scan(&t.ID, &t.Name, &t.Subject, &t.Body, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// Templates implements applications.MessageStore.
func (s *Store) Templates(ctx context.Context, tenantID string) ([]applications.Template, error) {
	out := []applications.Template{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+templateCols+` FROM hiring.app_email_templates WHERE tenant_id = $1::uuid ORDER BY lower(name)`, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: list templates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t applications.Template
			if err = rows.Scan(&t.ID, &t.Name, &t.Subject, &t.Body, &t.UpdatedAt); err != nil {
				return fmt.Errorf("appstore: scan template: %w", err)
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// Template implements applications.MessageStore.
func (s *Store) Template(ctx context.Context, tenantID, id string) (*applications.Template, error) {
	var t *applications.Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		t, err = scanTemplate(tx.QueryRow(ctx, `SELECT `+templateCols+` FROM hiring.app_email_templates WHERE id = $1::uuid AND tenant_id = $2::uuid`, id, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return t, err
}

// CreateTemplate implements applications.MessageStore.
func (s *Store) CreateTemplate(ctx context.Context, tenantID, actorID, name, subject, body string) (*applications.Template, error) {
	var t *applications.Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		t, err = scanTemplate(tx.QueryRow(ctx, `
INSERT INTO hiring.app_email_templates (tenant_id, name, subject, body, created_by)
VALUES ($1::uuid, $2, $3, $4, NULLIF($5, '')::uuid) RETURNING `+templateCols, tenantID, name, subject, body, actorID))
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrExists
			}
			return fmt.Errorf("appstore: create template: %w", err)
		}
		return nil
	})
	return t, err
}

// UpdateTemplate implements applications.MessageStore.
func (s *Store) UpdateTemplate(ctx context.Context, tenantID, id, name, subject, body string) (*applications.Template, error) {
	var t *applications.Template
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		t, err = scanTemplate(tx.QueryRow(ctx, `
UPDATE hiring.app_email_templates SET name = $3, subject = $4, body = $5, updated_at = now()
WHERE id = $1::uuid AND tenant_id = $2::uuid RETURNING `+templateCols, id, tenantID, name, subject, body))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrExists
			}
			return fmt.Errorf("appstore: update template: %w", err)
		}
		return nil
	})
	return t, err
}

// DeleteTemplate implements applications.MessageStore.
func (s *Store) DeleteTemplate(ctx context.Context, tenantID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hiring.app_email_templates WHERE id = $1::uuid AND tenant_id = $2::uuid`, id, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: delete template: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---- reply-to ----

// ReplyTo implements applications.MessageStore.
func (s *Store) ReplyTo(ctx context.Context, tenantID string) (string, error) {
	var addr string
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT reply_to FROM hiring.app_email_settings WHERE tenant_id = $1::uuid`, tenantID).Scan(&addr)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return addr, err
}

// SetReplyTo implements applications.MessageStore.
func (s *Store) SetReplyTo(ctx context.Context, tenantID, actorID, replyTo string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
INSERT INTO hiring.app_email_settings (tenant_id, reply_to, updated_by) VALUES ($1::uuid, $2, NULLIF($3, '')::uuid)
ON CONFLICT (tenant_id) DO UPDATE SET reply_to = EXCLUDED.reply_to, updated_by = EXCLUDED.updated_by, updated_at = now()`,
			tenantID, replyTo, actorID); err != nil {
			return fmt.Errorf("appstore: save reply-to: %w", err)
		}
		return nil
	})
}

// ---- messages ----

const insertMessageSQL = `
INSERT INTO hiring.app_messages
    (tenant_id, job_id, application_id, sender_id, template_id, subject, body, reply_to, company, status, scheduled_at)
SELECT $1::uuid, a.job_id, a.id, NULLIF($3, '')::uuid,
       (SELECT id FROM hiring.app_email_templates WHERE id = NULLIF($4, '')::uuid AND tenant_id = $1::uuid),
       $5, $6, $7, $8, $9, $10
FROM hiring.applications a WHERE a.id = $2::uuid AND a.tenant_id = $1::uuid AND a.job_id = $11::uuid
RETURNING id::text, COALESCE(sender_id::text, ''), COALESCE(template_id::text, ''), subject, body, status, scheduled_at, sent_at, created_at, last_error`

const quotaSQL = `
SELECT (SELECT count(*)::int FROM hiring.app_messages WHERE tenant_id = $1::uuid AND application_id = $2::uuid
          AND status <> 'cancelled' AND created_at > now() - interval '24 hours'),
       (SELECT count(*)::int FROM hiring.app_messages WHERE tenant_id = $1::uuid
          AND status <> 'cancelled' AND created_at > now() - interval '24 hours')`

func scanMessage(row pgx.Row) (*applications.Message, error) {
	var m applications.Message
	if err := row.Scan(&m.ID, &m.SenderID, &m.TemplateID, &m.Subject, &m.Body, &m.Status, &m.ScheduledAt, &m.SentAt, &m.CreatedAt, &m.Error); err != nil {
		return nil, err
	}
	return &m, nil
}

// insertMessage writes a message in tx. With enforce it first checks the daily limits.
func insertMessage(ctx context.Context, tx pgx.Tx, tenantID string, m *applications.NewMessage) (*applications.Message, error) {
	if m.Enforce {
		var perApp, perCompany int
		if err := tx.QueryRow(ctx, quotaSQL, tenantID, m.AppID).Scan(&perApp, &perCompany); err != nil {
			return nil, fmt.Errorf("appstore: message quota: %w", err)
		}
		if perApp >= applications.MaxPerApplication || perCompany >= applications.MaxPerCompany {
			return nil, applications.ErrQuota
		}
	}
	out, err := scanMessage(tx.QueryRow(ctx, insertMessageSQL, tenantID, m.AppID, m.SenderID, m.TemplateID, m.Subject, m.Body,
		m.ReplyTo, m.Company, m.Status, m.ScheduledAt, m.JobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("appstore: write message: %w", err)
	}
	return out, nil
}

// QueueMessage implements applications.MessageStore.
func (s *Store) QueueMessage(ctx context.Context, tenantID string, m *applications.NewMessage) (*applications.Message, error) {
	var out *applications.Message
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		if out, err = insertMessage(ctx, tx, tenantID, m); err != nil {
			return err
		}
		return insertEvent(ctx, tx, tenantID, m.JobID, m.AppID, m.SenderID, applications.EventMessage,
			map[string]any{"message_id": out.ID, "scheduled": m.Status == applications.MsgScheduled})
	})
	return out, err
}

// Messages implements applications.MessageStore.
func (s *Store) Messages(ctx context.Context, tenantID, jobID, appID string) ([]applications.Message, error) {
	out := []applications.Message{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
SELECT id::text, COALESCE(sender_id::text, ''), COALESCE(template_id::text, ''), subject, body, status, scheduled_at, sent_at, created_at, last_error
FROM hiring.app_messages WHERE tenant_id = $1::uuid AND application_id = $2::uuid
ORDER BY created_at DESC, id DESC LIMIT 100`, tenantID, appID)
		if err != nil {
			return fmt.Errorf("appstore: list messages: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m applications.Message
			if err = rows.Scan(&m.ID, &m.SenderID, &m.TemplateID, &m.Subject, &m.Body, &m.Status, &m.ScheduledAt, &m.SentAt, &m.CreatedAt, &m.Error); err != nil {
				return fmt.Errorf("appstore: scan message: %w", err)
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// CancelMessage implements applications.MessageStore.
func (s *Store) CancelMessage(ctx context.Context, tenantID, jobID, appID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		var status string
		// A message being sent right now holds a lease; it is no longer cancellable.
		err := tx.QueryRow(ctx, `
UPDATE hiring.app_messages SET status = 'cancelled'
WHERE id = $1::uuid AND application_id = $2::uuid AND tenant_id = $3::uuid
  AND status IN ('scheduled', 'queued') AND (lease_until IS NULL OR lease_until < now())
RETURNING status`, id, appID, tenantID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hiring.app_messages WHERE id = $1::uuid AND application_id = $2::uuid AND tenant_id = $3::uuid)`,
				id, appID, tenantID).Scan(&exists); qerr != nil {
				return fmt.Errorf("appstore: find message: %w", qerr)
			}
			if exists {
				return applications.ErrNotCancellable
			}
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: cancel message: %w", err)
		}
		return insertEvent(ctx, tx, tenantID, jobID, appID, "", applications.EventMessageCancelled, map[string]any{"message_id": id})
	})
}

// ---- the sender's side ----

// A message waits while its application is withdrawn: a candidate who withdrew is not written to.
const leaseMessagesSQL = `
UPDATE hiring.app_messages m SET attempts = m.attempts + 1, lease_until = $2::timestamptz + make_interval(secs => $4::float8)
FROM (SELECT m2.id FROM hiring.app_messages m2 JOIN hiring.applications a ON a.id = m2.application_id
      WHERE m2.tenant_id = $1::uuid AND m2.status IN ('scheduled', 'queued') AND m2.scheduled_at <= $2
        AND (m2.lease_until IS NULL OR m2.lease_until < $2) AND a.status <> 'withdrawn'
      ORDER BY m2.scheduled_at LIMIT $3 FOR UPDATE OF m2 SKIP LOCKED) due,
     hiring.applications a
WHERE m.id = due.id AND a.id = m.application_id
RETURNING m.id::text, a.candidate_email, a.candidate_name, m.company, m.subject, m.body, m.reply_to, m.attempts,
          COALESCE((SELECT title FROM hiring.job_postings j WHERE j.id = m.job_id), '')`

// LeaseMessages implements outbox.MessageStore.
func (s *Store) LeaseMessages(ctx context.Context, tenantID string, now time.Time, limit int, lease time.Duration) ([]outbox.DueMessage, error) {
	var out []outbox.DueMessage
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, leaseMessagesSQL, tenantID, now, limit, lease.Seconds())
		if err != nil {
			return fmt.Errorf("appstore: lease messages: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m outbox.DueMessage
			if err = rows.Scan(&m.ID, &m.Recipient, &m.Name, &m.Company, &m.Subject, &m.Body, &m.ReplyTo, &m.Attempts, &m.JobTitle); err != nil {
				return fmt.Errorf("appstore: scan message: %w", err)
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// MarkMessageSent implements outbox.MessageStore.
func (s *Store) MarkMessageSent(ctx context.Context, tenantID, id string, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var appID, jobID string
		err := tx.QueryRow(ctx, `UPDATE hiring.app_messages SET status = 'sent', sent_at = $3, last_error = '', lease_until = NULL
WHERE id = $1::uuid AND tenant_id = $2::uuid RETURNING application_id::text, job_id::text`, id, tenantID, now).Scan(&appID, &jobID)
		if err != nil {
			return fmt.Errorf("appstore: mark message sent: %w", err)
		}
		return insertEvent(ctx, tx, tenantID, jobID, appID, "", applications.EventMessageSent, map[string]any{"message_id": id})
	})
}

// MarkMessageFailed implements outbox.MessageStore.
func (s *Store) MarkMessageFailed(ctx context.Context, tenantID, id, reason string, final bool, next time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
UPDATE hiring.app_messages SET last_error = $3, status = CASE WHEN $4::boolean THEN 'failed' ELSE status END,
       lease_until = CASE WHEN $4::boolean THEN NULL ELSE $5::timestamptz END
WHERE id = $1::uuid AND tenant_id = $2::uuid`, id, tenantID, reason, final, next); err != nil {
			return fmt.Errorf("appstore: mark message failed: %w", err)
		}
		return nil
	})
}
