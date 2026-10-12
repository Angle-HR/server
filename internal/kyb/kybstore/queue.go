package kybstore

import (
	"context"
	"errors"
	"fmt"
)

// Queue is the global review queue (admin.verification_queue). It stores ids,
// region and status only; company details stay in the organization's region.
type Queue struct {
	DB     DB
	Region string // region the organization lives in, for example "uk"
}

// Enqueue opens a review item. Re-queueing an organization that already has an
// open item is a no-op.
func (q *Queue) Enqueue(ctx context.Context, organizationID, countryCode string) error {
	_, err := q.DB.Exec(ctx, `
		INSERT INTO admin.verification_queue (organization_id, region, country_code)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id) WHERE status = 'pending' DO NOTHING`,
		organizationID, q.Region, countryCode)
	if err != nil {
		return fmt.Errorf("kybstore: enqueue: %w", err)
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
		return fmt.Errorf("kybstore: resolve: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoPendingItem
	}
	return nil
}

// ErrNoPendingItem means there was no open review item to resolve.
var ErrNoPendingItem = errors.New("kybstore: no pending review item")
