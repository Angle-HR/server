// Package verifyqueue is the KYB review queue (admin.verification_queue, global database). It belongs to the
// identity service's admin module: org hands a case over with Enqueue (EnqueueVerification), a reviewer lists and
// closes items through the admin API, and org receives the decision back (OrgService.ApplyKybDecision). The table
// stores ids, region and status only; company details stay in the organization's region.
package verifyqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB is the global database as the queue needs it.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Queue reads and writes the review queue. Region is only used by Enqueue (the region the organization lives in,
// for example "uk").
type Queue struct {
	DB     DB
	Region string
}

// Item is one open review item.
type Item struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Region         string    `json:"region"`
	CountryCode    string    `json:"country_code"`
	SubmittedAt    time.Time `json:"submitted_at"`
	// CaseType is CaseBusiness (company verification) or CaseIndividual (identity review of a personal organization).
	CaseType string `json:"case_type"`
}

// Case types of a queue item.
const (
	CaseBusiness   = "business"
	CaseIndividual = "individual"
)

// ErrNoPendingItem means there was no open review item to resolve.
var ErrNoPendingItem = errors.New("verifyqueue: no pending review item")

// Enqueue opens a review item. Re-queueing an organization that already has an open item is a no-op.
func (q *Queue) Enqueue(ctx context.Context, organizationID, countryCode string) error {
	return q.EnqueueCase(ctx, organizationID, countryCode, CaseBusiness)
}

// EnqueueCase is Enqueue with a case type (CaseBusiness or CaseIndividual; empty means business).
func (q *Queue) EnqueueCase(ctx context.Context, organizationID, countryCode, caseType string) error {
	if caseType == "" {
		caseType = CaseBusiness
	}
	if caseType != CaseBusiness && caseType != CaseIndividual {
		return fmt.Errorf("verifyqueue: unknown case type %q", caseType)
	}
	_, err := q.DB.Exec(ctx, `
		INSERT INTO admin.verification_queue (organization_id, region, country_code, case_type)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (organization_id) WHERE status = 'pending' DO NOTHING`,
		organizationID, q.Region, countryCode, caseType)
	if err != nil {
		return fmt.Errorf("verifyqueue: enqueue: %w", err)
	}
	return nil
}

// Resolve closes the organization's open item with the reviewer's decision.
func (q *Queue) Resolve(ctx context.Context, organizationID, reviewerID string, approved bool) error {
	status := "rejected"
	if approved {
		status = "approved"
	}
	tag, err := q.DB.Exec(ctx, `
		UPDATE admin.verification_queue
		SET status = $2, decided_by = $3::uuid, decided_at = now()
		WHERE organization_id = $1 AND status = 'pending'`,
		organizationID, status, reviewerID)
	if err != nil {
		return fmt.Errorf("verifyqueue: resolve: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoPendingItem
	}
	return nil
}

// List returns open items oldest first. A non-empty region keeps that region only.
func (q *Queue) List(ctx context.Context, region string, limit, offset int) ([]Item, error) {
	rows, err := q.DB.Query(ctx, `
		SELECT id::text, organization_id::text, region, country_code, submitted_at, case_type
		FROM admin.verification_queue
		WHERE status = 'pending' AND ($3 = '' OR region = $3)
		ORDER BY submitted_at, id
		LIMIT $1 OFFSET $2`, limit, offset, region)
	if err != nil {
		return nil, fmt.Errorf("verifyqueue: list: %w", err)
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		if err := rows.Scan(&it.ID, &it.OrganizationID, &it.Region, &it.CountryCode, &it.SubmittedAt, &it.CaseType); err != nil {
			return nil, fmt.Errorf("verifyqueue: list: %w", err)
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// Pending finds the organization's open item. It returns ErrNoPendingItem when there is none.
func (q *Queue) Pending(ctx context.Context, organizationID string) (Item, error) {
	it := Item{OrganizationID: organizationID}
	err := q.DB.QueryRow(ctx, `
		SELECT id::text, region, country_code, submitted_at, case_type FROM admin.verification_queue
		WHERE organization_id = $1 AND status = 'pending'`, organizationID).
		Scan(&it.ID, &it.Region, &it.CountryCode, &it.SubmittedAt, &it.CaseType)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNoPendingItem
	}
	if err != nil {
		return Item{}, fmt.Errorf("verifyqueue: pending: %w", err)
	}
	return it, nil
}
