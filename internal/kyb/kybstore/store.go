// Package kybstore is the PostgreSQL implementation of the kyb persistence
// interfaces: the regional verification Store and the global review Queue.
package kybstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// DB is the subset of a pgx pool the stores need.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store persists verification state in the organization's regional database.
type Store struct{ DB DB }

const loadSQL = `
SELECT organization_id::text, country_code, registration_number, identifiers, legal_name_submitted,
       submitted_address, COALESCE(address_type, ''), tier, COALESCE(failure_reason, ''),
       COALESCE(registry_name, ''), COALESCE(registered_address, ''), attempts, checked_at, verified_at,
       COALESCE(reviewer_id::text, ''), notices_sent,
       (SELECT kyb_status FROM accounts.organizations WHERE id = v.organization_id)
FROM accounts.organization_verifications v
WHERE organization_id = $1`

// Load returns the organization's record, or nil, nil when it has none.
func (s *Store) Load(ctx context.Context, organizationID string) (*kyb.Record, error) {
	var (
		rec                     kyb.Record
		identifiers, addressRaw []byte
		addrType, reason        string
		tier, notices           int16
		attempts                int32
		status                  *string
	)
	err := s.DB.QueryRow(ctx, loadSQL, organizationID).Scan(
		&rec.OrganizationID, &rec.CountryCode, &rec.RegistrationNumber, &identifiers, &rec.LegalName,
		&addressRaw, &addrType, &tier, &reason, &rec.RegistryName, &rec.RegisteredAddress, &attempts,
		&rec.CheckedAt, &rec.VerifiedAt, &rec.ReviewerID, &notices, &status,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("kybstore: load: %w", err)
	}
	if err := json.Unmarshal(identifiers, &rec.Identifiers); err != nil {
		return nil, fmt.Errorf("kybstore: decode identifiers: %w", err)
	}
	if err := json.Unmarshal(addressRaw, &rec.Address); err != nil {
		return nil, fmt.Errorf("kybstore: decode address: %w", err)
	}
	rec.AddressType, rec.FailureReason = kyb.AddressType(addrType), kyb.FailureReason(reason)
	rec.Tier, rec.Attempts, rec.NoticesSent = int(tier), int(attempts), int(notices)
	rec.Status = kyb.StatusNotStarted
	if status != nil {
		rec.Status = kyb.Status(*status)
	}
	return &rec, nil
}

const upsertSQL = `
INSERT INTO accounts.organization_verifications
    (organization_id, country_code, registration_number, identifiers, legal_name_submitted, submitted_address,
     address_type, tier, failure_reason, registry_name, registered_address, attempts, checked_at, verified_at,
     reviewer_id, notices_sent)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), $12, $13, $14,
        NULLIF($15, '')::uuid, $16)
ON CONFLICT (organization_id) DO UPDATE SET
    country_code = EXCLUDED.country_code, registration_number = EXCLUDED.registration_number,
    identifiers = EXCLUDED.identifiers, legal_name_submitted = EXCLUDED.legal_name_submitted,
    submitted_address = EXCLUDED.submitted_address, address_type = EXCLUDED.address_type, tier = EXCLUDED.tier,
    failure_reason = EXCLUDED.failure_reason, registry_name = EXCLUDED.registry_name,
    registered_address = EXCLUDED.registered_address, attempts = EXCLUDED.attempts,
    checked_at = EXCLUDED.checked_at, verified_at = EXCLUDED.verified_at, reviewer_id = EXCLUDED.reviewer_id,
    notices_sent = EXCLUDED.notices_sent,
    deletion_flagged_at = CASE
        WHEN EXCLUDED.failure_reason IS NULL
          OR EXCLUDED.attempts > accounts.organization_verifications.attempts THEN NULL
        ELSE accounts.organization_verifications.deletion_flagged_at END`

const statusSQL = `
UPDATE accounts.organizations
SET kyb_status = $2::text, kyb_verified_at = CASE WHEN $2::text = 'verified' THEN $3::timestamptz ELSE NULL END
WHERE id = $1`

const eventSQL = `
INSERT INTO accounts.organization_verification_events
    (organization_id, event, status, failure_reason, actor_id, detail, created_at)
VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, '')::uuid, $6, $7)`

// Save writes the record, the organization's kyb_status and the events in one transaction.
func (s *Store) Save(ctx context.Context, rec *kyb.Record, events ...kyb.Event) (err error) {
	identifiers, err := json.Marshal(nonNilMap(rec.Identifiers))
	if err != nil {
		return fmt.Errorf("kybstore: encode identifiers: %w", err)
	}
	address, err := json.Marshal(rec.Address)
	if err != nil {
		return fmt.Errorf("kybstore: encode address: %w", err)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("kybstore: begin: %w", err)
	}
	defer func() {
		if err != nil {
			besteffort.Log(ctx, "kybstore.rollback", tx.Rollback(ctx))
		}
	}()

	if _, err = tx.Exec(ctx, upsertSQL, rec.OrganizationID, rec.CountryCode, rec.RegistrationNumber, identifiers,
		rec.LegalName, address, string(rec.AddressType), rec.Tier, string(rec.FailureReason),
		rec.RegistryName, rec.RegisteredAddress, rec.Attempts, rec.CheckedAt, rec.VerifiedAt,
		rec.ReviewerID, rec.NoticesSent); err != nil {
		return fmt.Errorf("kybstore: save record: %w", err)
	}
	if _, err = tx.Exec(ctx, statusSQL, rec.OrganizationID, string(rec.Status), rec.VerifiedAt); err != nil {
		return fmt.Errorf("kybstore: save status: %w", err)
	}
	for i := range events {
		ev := &events[i]
		detail, derr := json.Marshal(nonNilAnyMap(ev.Detail))
		if derr != nil {
			err = fmt.Errorf("kybstore: encode event detail: %w", derr)
			return err
		}
		if _, err = tx.Exec(ctx, eventSQL, ev.OrganizationID, ev.Name, string(ev.Status), string(ev.Reason),
			ev.ActorID, detail, eventTime(ev.At)); err != nil {
			return fmt.Errorf("kybstore: save event: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("kybstore: commit: %w", err)
	}
	return nil
}

func eventTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilAnyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
