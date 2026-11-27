package appstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
)

var _ applications.OfferStore = (*Store)(nil)

// ---- templates ----

// OfferTemplates implements applications.OfferStore.
func (s *Store) OfferTemplates(ctx context.Context, tenantID string) ([]applications.OfferTemplate, error) {
	out := []applications.OfferTemplate{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, name, body, updated_at FROM hiring.app_offer_templates WHERE tenant_id = $1::uuid ORDER BY lower(name)`, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: list offer templates: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var t applications.OfferTemplate
			if err = rows.Scan(&t.ID, &t.Name, &t.Body, &t.UpdatedAt); err != nil {
				return fmt.Errorf("appstore: scan offer template: %w", err)
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// OfferTemplate implements applications.OfferStore.
func (s *Store) OfferTemplate(ctx context.Context, tenantID, id string) (*applications.OfferTemplate, error) {
	var t applications.OfferTemplate
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text, name, body, updated_at FROM hiring.app_offer_templates WHERE id = $1::uuid AND tenant_id = $2::uuid`,
			id, tenantID).Scan(&t.ID, &t.Name, &t.Body, &t.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateOfferTemplate implements applications.OfferStore.
func (s *Store) CreateOfferTemplate(ctx context.Context, tenantID, actorID, name, body string) (*applications.OfferTemplate, error) {
	var t applications.OfferTemplate
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO hiring.app_offer_templates (tenant_id, name, body, created_by) VALUES ($1::uuid, $2, $3, NULLIF($4, '')::uuid)
RETURNING id::text, name, body, updated_at`, tenantID, name, body, actorID).Scan(&t.ID, &t.Name, &t.Body, &t.UpdatedAt)
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrExists
			}
			return fmt.Errorf("appstore: create offer template: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// UpdateOfferTemplate implements applications.OfferStore.
func (s *Store) UpdateOfferTemplate(ctx context.Context, tenantID, id, name, body string) (*applications.OfferTemplate, error) {
	var t applications.OfferTemplate
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE hiring.app_offer_templates SET name = $3, body = $4, updated_at = now()
WHERE id = $1::uuid AND tenant_id = $2::uuid RETURNING id::text, name, body, updated_at`, id, tenantID, name, body).Scan(&t.ID, &t.Name, &t.Body, &t.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrExists
			}
			return fmt.Errorf("appstore: update offer template: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// DeleteOfferTemplate implements applications.OfferStore.
func (s *Store) DeleteOfferTemplate(ctx context.Context, tenantID, id string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM hiring.app_offer_templates WHERE id = $1::uuid AND tenant_id = $2::uuid`, id, tenantID)
		if err != nil {
			return fmt.Errorf("appstore: delete offer template: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// ---- offers ----

// An offer that was sent and not answered by its expiry is shown as expired, and is written as expired the next time
// anything touches it.
const offerCols = `o.id::text, o.job_id::text, o.application_id::text, o.title, o.body, o.compensation,
       COALESCE(to_char(o.start_date, 'YYYY-MM-DD'), ''),
       CASE WHEN o.status IN ('sent', 'viewed') AND o.expires_at < $NOW THEN 'expired' ELSE o.status END,
       o.expires_at, o.sent_at, o.viewed_at, o.signed_at, o.declined_at, o.decline_reason, o.signed_name, o.pdf_key <> '',
       COALESCE(o.created_by::text, ''), o.created_at, o.updated_at, o.sequence, o.doc_hash, o.pdf_key, COALESCE(o.template_id::text, '')`

func offerSelect(nowParam string) string {
	return replaceNow(offerCols, nowParam)
}

func replaceNow(s, p string) string {
	out := ""
	for i := 0; i < len(s); {
		if len(s)-i >= 4 && s[i:i+4] == "$NOW" {
			out += p
			i += 4
			continue
		}
		out += string(s[i])
		i++
	}
	return out
}

func scanOffer(row pgx.Row) (*applications.Offer, error) {
	var o applications.Offer
	if err := row.Scan(&o.ID, &o.JobID, &o.ApplicationID, &o.Title, &o.Body, &o.Compensation, &o.StartDate, &o.Status, &o.ExpiresAt,
		&o.SentAt, &o.ViewedAt, &o.SignedAt, &o.DeclinedAt, &o.DeclineReason, &o.SignedName, &o.HasDocument, &o.CreatedBy, &o.CreatedAt,
		&o.UpdatedAt, &o.Sequence, &o.DocHash, &o.PDFKey, &o.TemplateID); err != nil {
		return nil, err
	}
	return &o, nil
}

func insertOfferEvent(ctx context.Context, tx pgx.Tx, tenantID, offerID, appID, actorID, typ string, m applications.Meta, data map[string]any) error {
	if data == nil {
		data = map[string]any{}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("appstore: encode offer event: %w", err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hiring.app_offer_events (tenant_id, offer_id, application_id, actor_id, type, ip, user_agent, data)
VALUES ($1::uuid, $2::uuid, $3::uuid, NULLIF($4, '')::uuid, $5, $6, $7, $8::jsonb)`, tenantID, offerID, appID, actorID, typ, m.IP, m.UserAgent, raw); err != nil {
		return fmt.Errorf("appstore: write offer event: %w", err)
	}
	return nil
}

// expireStale writes the expiry of offers of an application that ran out.
func expireStale(ctx context.Context, tx pgx.Tx, tenantID, appID string, now time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE hiring.app_offers SET status = 'expired', token_hash = '', updated_at = $3
WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND status IN ('sent', 'viewed') AND expires_at < $3 RETURNING id::text`, tenantID, appID, now)
	if err != nil {
		return fmt.Errorf("appstore: expire offers: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = insertOfferEvent(ctx, tx, tenantID, id, appID, "", applications.OfferEvExpired, applications.Meta{}, nil); err != nil {
			return err
		}
	}
	return nil
}

// Offers implements applications.OfferStore.
func (s *Store) Offers(ctx context.Context, tenantID, jobID, appID string, now time.Time) ([]applications.Offer, error) {
	out := []applications.Offer{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+offerSelect("$3")+` FROM hiring.app_offers o
WHERE o.tenant_id = $1::uuid AND o.application_id = $2::uuid ORDER BY o.created_at DESC, o.id DESC LIMIT 50`, tenantID, appID, now)
		if err != nil {
			return fmt.Errorf("appstore: list offers: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOffer(rows)
			if err != nil {
				return fmt.Errorf("appstore: scan offer: %w", err)
			}
			out = append(out, *o)
		}
		return rows.Err()
	})
	return out, err
}

// Offer implements applications.OfferStore.
func (s *Store) Offer(ctx context.Context, tenantID, jobID, appID, id string, now time.Time) (*applications.Offer, error) {
	var o *applications.Offer
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		o, err = scanOffer(tx.QueryRow(ctx, `SELECT `+offerSelect("$5")+` FROM hiring.app_offers o
WHERE o.id = $1::uuid AND o.tenant_id = $2::uuid AND o.job_id = $3::uuid AND o.application_id = $4::uuid`, id, tenantID, jobID, appID, now))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return o, err
}

// CreateOffer implements applications.OfferStore.
func (s *Store) CreateOffer(ctx context.Context, tenantID, actorID string, o *applications.Offer) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, o.JobID, o.ApplicationID); err != nil {
			return err
		}
		if err := expireStale(ctx, tx, tenantID, o.ApplicationID, time.Now().UTC()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO hiring.app_offers (id, tenant_id, job_id, application_id, created_by, template_id, title, body, compensation, start_date, status)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, NULLIF($5, '')::uuid,
        (SELECT id FROM hiring.app_offer_templates WHERE id = NULLIF($6, '')::uuid AND tenant_id = $2::uuid), $7, $8, $9, NULLIF($10, '')::date, 'draft')`,
			o.ID, tenantID, o.JobID, o.ApplicationID, actorID, o.TemplateID, o.Title, o.Body, o.Compensation, o.StartDate)
		if err != nil {
			if mapPgError(err) == ErrConflict {
				return applications.ErrOfferLive
			}
			return fmt.Errorf("appstore: insert offer: %w", err)
		}
		o.CreatedAt, o.UpdatedAt = time.Now().UTC(), time.Now().UTC()
		return insertOfferEvent(ctx, tx, tenantID, o.ID, o.ApplicationID, actorID, applications.OfferEvCreated, applications.Meta{}, nil)
	})
}

// SaveDraft implements applications.OfferStore.
func (s *Store) SaveDraft(ctx context.Context, tenantID, actorID string, o *applications.Offer, prevSeq int) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hiring.app_offers SET title = $5, body = $6, compensation = $7, start_date = NULLIF($8, '')::date, sequence = $9, updated_at = now()
WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid AND status = 'draft' AND sequence = $4`,
			o.ID, tenantID, o.JobID, prevSeq, o.Title, o.Body, o.Compensation, o.StartDate, o.Sequence)
		if err != nil {
			return fmt.Errorf("appstore: save offer: %w", err)
		}
		if tag.RowsAffected() == 0 {
			var status string
			if qerr := tx.QueryRow(ctx, `SELECT status FROM hiring.app_offers WHERE id = $1::uuid AND tenant_id = $2::uuid`, o.ID, tenantID).Scan(&status); qerr != nil {
				return ErrNotFound
			}
			if status != "draft" {
				return applications.ErrOfferNotDraft
			}
			return applications.ErrStale
		}
		return insertOfferEvent(ctx, tx, tenantID, o.ID, o.ApplicationID, actorID, applications.OfferEvEdited, applications.Meta{}, nil)
	})
}

// SendOffer implements applications.OfferStore.
func (s *Store) SendOffer(ctx context.Context, tenantID, actorID string, o *applications.Offer, tokenHash string, msg *applications.NewMessage, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var was string
		err := tx.QueryRow(ctx, `UPDATE hiring.app_offers x SET status = 'sent', token_hash = $4, doc_hash = $5, expires_at = $6, sent_at = $7, sent_by = NULLIF($8, '')::uuid, updated_at = $7
FROM (SELECT id, status FROM hiring.app_offers WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid FOR UPDATE) old
WHERE x.id = old.id AND x.status IN ('draft', 'sent', 'viewed') RETURNING old.status`,
			o.ID, tenantID, o.JobID, tokenHash, o.DocHash, o.ExpiresAt, now, actorID).Scan(&was)
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hiring.app_offers WHERE id = $1::uuid AND tenant_id = $2::uuid)`, o.ID, tenantID).Scan(&exists); qerr != nil || !exists {
				return ErrNotFound
			}
			return applications.ErrOfferClosed
		}
		if err != nil {
			return fmt.Errorf("appstore: send offer: %w", mapPgError(err))
		}
		if err = insertOfferEvent(ctx, tx, tenantID, o.ID, o.ApplicationID, actorID, applications.OfferEvSent, applications.Meta{},
			map[string]any{"expires_at": o.ExpiresAt, "doc_hash": o.DocHash, "resent": was != "draft"}); err != nil {
			return err
		}
		out, err := insertMessage(ctx, tx, tenantID, msg)
		if err != nil {
			return err
		}
		return insertEvent(ctx, tx, tenantID, o.JobID, o.ApplicationID, actorID, applications.EventOfferSent, map[string]any{"offer_id": o.ID, "message_id": out.ID})
	})
}

// WithdrawOffer implements applications.OfferStore.
func (s *Store) WithdrawOffer(ctx context.Context, tenantID, actorID, jobID, appID, id string, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'withdrawn', token_hash = '', updated_at = $5
WHERE id = $1::uuid AND tenant_id = $2::uuid AND job_id = $3::uuid AND application_id = $4::uuid AND status IN ('draft', 'sent', 'viewed')`,
			id, tenantID, jobID, appID, now)
		if err != nil {
			return fmt.Errorf("appstore: withdraw offer: %w", err)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hiring.app_offers WHERE id = $1::uuid AND tenant_id = $2::uuid AND application_id = $3::uuid)`, id, tenantID, appID).Scan(&exists); qerr != nil || !exists {
				return ErrNotFound
			}
			return applications.ErrOfferClosed
		}
		return insertOfferEvent(ctx, tx, tenantID, id, appID, actorID, applications.OfferEvWithdrawn, applications.Meta{}, nil)
	})
}

// OfferEvents implements applications.OfferStore.
func (s *Store) OfferEvents(ctx context.Context, tenantID, jobID, appID, id string) ([]applications.OfferEvent, error) {
	out := []applications.OfferEvent{}
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := requireApp(ctx, tx, tenantID, jobID, appID); err != nil {
			return err
		}
		var err error
		out, err = loadOfferEvents(ctx, tx, tenantID, id, appID)
		return err
	})
	return out, err
}

func loadOfferEvents(ctx context.Context, tx pgx.Tx, tenantID, offerID, appID string) ([]applications.OfferEvent, error) {
	rows, err := tx.Query(ctx, `SELECT id, COALESCE(actor_id::text, ''), type, ip, user_agent, data, created_at FROM hiring.app_offer_events
WHERE tenant_id = $1::uuid AND offer_id = $2::uuid AND application_id = $3::uuid ORDER BY id LIMIT 500`, tenantID, offerID, appID)
	if err != nil {
		return nil, fmt.Errorf("appstore: offer events: %w", err)
	}
	defer rows.Close()
	out := []applications.OfferEvent{}
	for rows.Next() {
		var e applications.OfferEvent
		if err = rows.Scan(&e.ID, &e.ActorID, &e.Type, &e.IP, &e.UserAgent, &e.Data, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("appstore: scan offer event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- the candidate's side ----

// OfferByToken implements applications.OfferStore. An unknown or revoked token is ErrNotFound.
func (s *Store) OfferByToken(ctx context.Context, tenantID, tokenHash string) (*applications.OfferRef, error) {
	var r applications.OfferRef
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT id::text, job_id::text, application_id::text, status FROM hiring.app_offers
WHERE tenant_id = $1::uuid AND token_hash = $2 AND $2 <> ''`, tenantID, tokenHash).Scan(&r.OfferID, &r.JobID, &r.AppID, &r.Status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

const viewOfferSQL = `
SELECT o.id::text, o.application_id::text, o.status, o.title, o.body, o.compensation, COALESCE(to_char(o.start_date, 'YYYY-MM-DD'), ''),
       o.expires_at, o.signed_at, o.signed_name, o.pdf_key <> '', a.candidate_name
FROM hiring.app_offers o JOIN hiring.applications a ON a.id = o.application_id
WHERE o.tenant_id = $1::uuid AND o.token_hash = $2 AND $2 <> '' FOR UPDATE OF o`

// ViewOffer implements applications.OfferStore.
func (s *Store) ViewOffer(ctx context.Context, tenantID, tokenHash string, m applications.Meta, now time.Time) (*applications.PublicOffer, error) {
	var p applications.PublicOffer
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var offerID, appID string
		err := tx.QueryRow(ctx, viewOfferSQL, tenantID, tokenHash).Scan(&offerID, &appID, &p.Status, &p.Title, &p.Body, &p.Compensation, &p.StartDate,
			&p.ExpiresAt, &p.SignedAt, &p.SignedName, &p.HasDocument, &p.CandidateName)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: view offer: %w", err)
		}
		p.ConsentText = applications.ConsentText
		if (p.Status == applications.OfferSent || p.Status == applications.OfferViewed) && p.ExpiresAt != nil && p.ExpiresAt.Before(now) {
			if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'expired', updated_at = $2 WHERE id = $1::uuid`, offerID, now); err != nil {
				return fmt.Errorf("appstore: expire offer: %w", err)
			}
			if err = insertOfferEvent(ctx, tx, tenantID, offerID, appID, "", applications.OfferEvExpired, m, nil); err != nil {
				return err
			}
			p.Status = applications.OfferExpired
		}
		if p.Status == applications.OfferSent || p.Status == applications.OfferViewed {
			if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'viewed', viewed_at = COALESCE(viewed_at, $2), updated_at = $2 WHERE id = $1::uuid`, offerID, now); err != nil {
				return fmt.Errorf("appstore: mark offer viewed: %w", err)
			}
			if err = insertOfferEvent(ctx, tx, tenantID, offerID, appID, "", applications.OfferEvViewed, m, nil); err != nil {
				return err
			}
			p.Status, p.CanRespond = applications.OfferViewed, true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

const lockOfferSQL = `
SELECT o.id::text, o.job_id::text, o.application_id::text, o.status, o.expires_at, o.title, o.body, o.compensation,
       COALESCE(to_char(o.start_date, 'YYYY-MM-DD'), ''), o.doc_hash, COALESCE(o.sent_by::text, ''), a.status
FROM hiring.app_offers o JOIN hiring.applications a ON a.id = o.application_id
WHERE o.tenant_id = $1::uuid AND o.token_hash = $2 AND $2 <> '' FOR UPDATE OF o, a`

// SignOffer implements applications.OfferStore: the signature, the audit entry, the hire and the confirmation message
// are one transaction.
func (s *Store) SignOffer(ctx context.Context, tenantID string, r *applications.SignRecord) (*applications.Offer, error) {
	var out applications.Offer
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var (
			status, appStatus, startDate, docHash, sentBy string
			expires                                       *time.Time
		)
		err := tx.QueryRow(ctx, lockOfferSQL, tenantID, r.TokenHash).Scan(&out.ID, &out.JobID, &out.ApplicationID, &status, &expires, &out.Title,
			&out.Body, &out.Compensation, &startDate, &docHash, &sentBy, &appStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: lock offer: %w", err)
		}
		out.StartDate = startDate
		if status != applications.OfferSent && status != applications.OfferViewed {
			return applications.ErrOfferClosed
		}
		if expires != nil && expires.Before(r.Now) {
			if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'expired', updated_at = $2 WHERE id = $1::uuid`, out.ID, r.Now); err != nil {
				return fmt.Errorf("appstore: expire offer: %w", err)
			}
			if err = insertOfferEvent(ctx, tx, tenantID, out.ID, out.ApplicationID, "", applications.OfferEvExpired, r.Meta, nil); err != nil {
				return err
			}
			return applications.ErrOfferClosed
		}
		// The terms must be exactly what was sent.
		if applications.OfferDocHash(out.Title, out.Body, out.Compensation, startDate) != docHash {
			return applications.ErrOfferUnavailable
		}
		// The application must still be in review: it may have been decided or withdrawn since the offer went out.
		if appStatus != applications.StatusInReview {
			if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'withdrawn', token_hash = '', updated_at = $2 WHERE id = $1::uuid`, out.ID, r.Now); err != nil {
				return fmt.Errorf("appstore: withdraw offer: %w", err)
			}
			if err = insertOfferEvent(ctx, tx, tenantID, out.ID, out.ApplicationID, "", applications.OfferEvWithdrawn, r.Meta, map[string]any{"reason": "application " + appStatus}); err != nil {
				return err
			}
			return applications.ErrOfferUnavailable
		}
		// One use: the link stops working once the offer is signed.
		if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'signed', signed_at = $2, signed_name = $3, token_hash = '', updated_at = $2 WHERE id = $1::uuid`,
			out.ID, r.Now, r.Name); err != nil {
			return fmt.Errorf("appstore: sign offer: %w", err)
		}
		if err = insertOfferEvent(ctx, tx, tenantID, out.ID, out.ApplicationID, "", applications.OfferEvSigned, r.Meta, map[string]any{
			"name": r.Name, "doc_hash": docHash, "consent_version": applications.ConsentVersion, "consent_hash": r.ConsentHash,
		}); err != nil {
			return err
		}
		// Signing hires the candidate. The person who sent the offer is the one accountable for the decision; the
		// candidate gets the confirmation below, not the standard hire email.
		ch := &applications.Change{AppID: out.ApplicationID, JobID: out.JobID, ActorID: sentBy, From: applications.StatusInReview,
			To: applications.StatusHired, Note: "Offer signed", Now: r.Now, RetentionMonths: r.RetentionMonths}
		if ch.ActorID == "" {
			return applications.ErrOfferUnavailable
		}
		if err = s.applyChangeTx(ctx, tx, tenantID, ch); err != nil {
			return err
		}
		if r.Confirmation != nil {
			if err = queueWith(ctx, tx, tenantID, "", r.Confirmation); err != nil {
				return err
			}
		}
		t := r.Now
		out.Status, out.SignedAt, out.SignedName = applications.OfferSigned, &t, r.Name
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeclineOffer implements applications.OfferStore.
func (s *Store) DeclineOffer(ctx context.Context, tenantID, tokenHash, reason string, m applications.Meta, now time.Time) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var offerID, appID, status string
		var expires *time.Time
		err := tx.QueryRow(ctx, `SELECT id::text, application_id::text, status, expires_at FROM hiring.app_offers
WHERE tenant_id = $1::uuid AND token_hash = $2 AND $2 <> '' FOR UPDATE`, tenantID, tokenHash).Scan(&offerID, &appID, &status, &expires)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("appstore: lock offer: %w", err)
		}
		if (status != applications.OfferSent && status != applications.OfferViewed) || (expires != nil && expires.Before(now)) {
			return applications.ErrOfferClosed
		}
		if _, err = tx.Exec(ctx, `UPDATE hiring.app_offers SET status = 'declined', declined_at = $2, decline_reason = $3, token_hash = '', updated_at = $2 WHERE id = $1::uuid`,
			offerID, now, reason); err != nil {
			return fmt.Errorf("appstore: decline offer: %w", err)
		}
		// The reason is the candidate's words: it is kept on the offer, not on the timeline.
		return insertOfferEvent(ctx, tx, tenantID, offerID, appID, "", applications.OfferEvDeclined, m, map[string]any{"has_reason": reason != ""})
	})
}

// DocumentData implements applications.OfferStore.
func (s *Store) DocumentData(ctx context.Context, tenantID, offerID string) (*applications.DocumentData, error) {
	var d applications.DocumentData
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		o, err := scanOffer(tx.QueryRow(ctx, `SELECT `+offerSelect("now()")+` FROM hiring.app_offers o WHERE o.id = $1::uuid AND o.tenant_id = $2::uuid`, offerID, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if o.Status != applications.OfferSigned {
			return ErrNotFound
		}
		d.Offer = *o
		if err = tx.QueryRow(ctx, `SELECT candidate_name FROM hiring.applications WHERE id = $1::uuid AND tenant_id = $2::uuid`, o.ApplicationID, tenantID).Scan(&d.CandidateName); err != nil {
			return fmt.Errorf("appstore: candidate of offer: %w", err)
		}
		if d.Events, err = loadOfferEvents(ctx, tx, tenantID, offerID, o.ApplicationID); err != nil {
			return err
		}
		for _, e := range d.Events {
			if e.Type == applications.OfferEvSigned {
				d.SignerIP, d.SignerAgent = e.IP, e.UserAgent
				var data struct {
					ConsentHash string `json:"consent_hash"`
				}
				_ = json.Unmarshal(e.Data, &data)
				d.ConsentHash = data.ConsentHash
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// SetOfferDocument implements applications.OfferStore.
func (s *Store) SetOfferDocument(ctx context.Context, tenantID, offerID, key string) error {
	return s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE hiring.app_offers SET pdf_key = $3 WHERE id = $1::uuid AND tenant_id = $2::uuid`, offerID, tenantID, key); err != nil {
			return fmt.Errorf("appstore: save offer document: %w", err)
		}
		return nil
	})
}

// withdrawLiveOffers withdraws the offers of an application that are still out, when the application is decided.
func withdrawLiveOffers(ctx context.Context, tx pgx.Tx, tenantID, appID string, now time.Time) error {
	rows, err := tx.Query(ctx, `UPDATE hiring.app_offers SET status = 'withdrawn', token_hash = '', updated_at = $3
WHERE tenant_id = $1::uuid AND application_id = $2::uuid AND status IN ('draft', 'sent', 'viewed') RETURNING id::text`, tenantID, appID, now)
	if err != nil {
		return fmt.Errorf("appstore: withdraw offers: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err = insertOfferEvent(ctx, tx, tenantID, id, appID, "", applications.OfferEvWithdrawn, applications.Meta{}, map[string]any{"reason": "application decided"}); err != nil {
			return err
		}
	}
	return nil
}
