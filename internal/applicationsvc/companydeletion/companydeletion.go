// Package companydeletion deletes everything the applications service holds about a company when org announces
// that the company was deleted. Event contract (also in docs/microservices/applications.md):
//
//	type          org.company.deleted.v1   (subject filter org.company.deleted.>)
//	envelope      CompanyID = the company, Region = its region, Subject = the company id
//	data (JSON)   {"company_id": "...", "region": "uk", "deleted_at": "RFC 3339", "reason": "free text, optional"}
//	partition key the company id
//
// Nothing emits it yet: org does not delete companies for real today. When it does, it must publish this event from
// the transaction that deletes the company (outbox), because the foreign keys that used to cascade from
// accounts.organizations were dropped in migration 000019.
//
// The consumer is "applications-org" (events_inbox consumer name). Redelivery is harmless: the handler is wrapped in
// the inbox, and every step is a delete that finds nothing the second time.
package companydeletion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
	"github.com/Angle-HR/server/pkg/platform/blob"
)

// Contract constants.
const (
	Consumer      = "applications-org"
	Type          = "org.company.deleted.v1"
	SubjectFilter = "org.company.deleted.>"
	// TypeReactivated lifts the tombstone a deletion left (org.company.reactivated.v1). The removed data does not
	// come back; the company's next job events are accepted again.
	TypeReactivated = "org.company.reactivated.v1"
	// ReactivatedFilter is the subject filter of TypeReactivated.
	ReactivatedFilter = "org.company.reactivated.>"
)

// Payload is the data of org.company.deleted.v1. Fields missing here fall back to the envelope.
type Payload struct {
	CompanyID string    `json:"company_id"`
	Region    string    `json:"region"`
	DeletedAt time.Time `json:"deleted_at"`
	Reason    string    `json:"reason,omitempty"`
}

// Handler deletes a deleted company's applications data in the region named by the event.
type Handler struct {
	// Pools returns the database of a region (jobmodel.ErrUnknownRegion for an unknown one).
	Pools jobmodel.Pools
	// Storage returns the bucket of a region. Nil, or an error, skips object deletion for that region and retries
	// the event (the files must not be orphaned).
	Storage func(region string) (blob.BlobStore, error)
	Log     *slog.Logger
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// The company's rows, children first. Audit and compliance rows (hiring.app_audit_events, app_compliance_log) are
// kept on purpose: they hold ids only, never candidate data, and the compliance log is append-only. A final
// compliance row records the deletion. The job read model rows (hiring.app_jobs, app_job_forms) are the
// company's copy of its jobs and go too.
var deleteSQL = []string{
	`INSERT INTO hiring.app_deleted_companies (company_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`,
	`DELETE FROM hiring_restricted.special_answers WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.review_tasks WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.application_files WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.email_outbox WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.reapply_blocks WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.job_applicant_counts WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_events WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_notes WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_application_tags WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_follows WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.applications WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_tags WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_job_stages WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.org_settings WHERE tenant_id = $1::uuid`,
	`DELETE FROM hiring.app_job_forms WHERE company_id = $1::uuid`,
	`DELETE FROM hiring.app_jobs WHERE company_id = $1::uuid`,
}

const logSQL = `
INSERT INTO hiring.app_compliance_log (job_id, tenant_id, gate_id, severity, event)
VALUES ('00000000-0000-0000-0000-000000000000'::uuid, $1::uuid, 'retention', 'informational', $2)`

// Handle is the events.Handler. A malformed event or an unknown region is dead-lettered; a database or storage
// failure is retried.
func (h *Handler) Handle(ctx context.Context, e events.Event) error {
	if e.Type == TypeReactivated {
		return h.handleReactivated(ctx, e)
	}
	if e.Type != Type {
		return events.Terminal(fmt.Errorf("companydeletion: unexpected event type %q", e.Type))
	}
	var p Payload
	if err := e.DecodeJSON(&p); err != nil {
		return events.Terminal(fmt.Errorf("companydeletion: decode %s: %w", e.ID, err))
	}
	if p.CompanyID == "" {
		p.CompanyID = e.CompanyID
	}
	if p.Region == "" {
		p.Region = e.Region
	}
	if !uuidRE.MatchString(p.CompanyID) {
		return events.Terminal(fmt.Errorf("companydeletion: %s: company id %q is not a uuid", e.ID, p.CompanyID))
	}
	if p.Region == "" {
		return events.Terminal(fmt.Errorf("companydeletion: %s: no region", e.ID))
	}
	db, err := h.Pools(p.Region)
	if errors.Is(err, jobmodel.ErrUnknownRegion) {
		return events.Terminal(fmt.Errorf("companydeletion: %s: region %q has no database", e.ID, p.Region))
	}
	if err != nil {
		return fmt.Errorf("companydeletion: database for region %q: %w", p.Region, err)
	}

	// Objects first: the rows say where they are. Deleting an object twice is fine, so a failure after this point
	// (or before the commit) just repeats it on the retry.
	if err := h.deleteObjects(ctx, db, p); err != nil {
		return err
	}
	reason := "company_deleted: all applications data of the company removed"
	if p.Reason != "" {
		reason += " (" + p.Reason + ")"
	}
	return inbox.Handle(db, Consumer, func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, p.CompanyID); err != nil {
			return fmt.Errorf("companydeletion: set tenant: %w", err)
		}
		for _, q := range deleteSQL {
			if _, err := tx.Exec(ctx, q, p.CompanyID); err != nil {
				return fmt.Errorf("companydeletion: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, logSQL, p.CompanyID, reason); err != nil {
			return fmt.Errorf("companydeletion: log deletion: %w", err)
		}
		return nil
	})(ctx, e)
}

// handleReactivated removes the company's tombstone so its job events are projected again.
func (h *Handler) handleReactivated(ctx context.Context, e events.Event) error {
	var p Payload
	if err := e.DecodeJSON(&p); err != nil {
		return events.Terminal(fmt.Errorf("companydeletion: decode %s: %w", e.ID, err))
	}
	if p.CompanyID == "" {
		p.CompanyID = e.CompanyID
	}
	if p.Region == "" {
		p.Region = e.Region
	}
	if !uuidRE.MatchString(p.CompanyID) || p.Region == "" {
		return events.Terminal(fmt.Errorf("companydeletion: %s: company id %q or region %q invalid", e.ID, p.CompanyID, p.Region))
	}
	db, err := h.Pools(p.Region)
	if errors.Is(err, jobmodel.ErrUnknownRegion) {
		return events.Terminal(fmt.Errorf("companydeletion: %s: region %q has no database", e.ID, p.Region))
	}
	if err != nil {
		return fmt.Errorf("companydeletion: database for region %q: %w", p.Region, err)
	}
	return inbox.Handle(db, Consumer, func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		if _, err := tx.Exec(ctx, `DELETE FROM hiring.app_deleted_companies WHERE company_id = $1::uuid`, p.CompanyID); err != nil {
			return fmt.Errorf("companydeletion: lift tombstone: %w", err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, p.CompanyID); err != nil {
			return fmt.Errorf("companydeletion: set tenant: %w", err)
		}
		if _, err := tx.Exec(ctx, logSQL, p.CompanyID, "company_reactivated: tombstone lifted, removed data is not restored"); err != nil {
			return fmt.Errorf("companydeletion: log reactivation: %w", err)
		}
		return nil
	})(ctx, e)
}

// fileRefs lists the company's uploaded files.
const fileRefsSQL = `SELECT id::text, job_id::text FROM hiring.application_files WHERE tenant_id = $1::uuid`

type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

func (h *Handler) deleteObjects(ctx context.Context, db beginner, p Payload) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("companydeletion: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, p.CompanyID); err != nil {
		return fmt.Errorf("companydeletion: set tenant: %w", err)
	}
	rows, err := tx.Query(ctx, fileRefsSQL, p.CompanyID)
	if err != nil {
		return fmt.Errorf("companydeletion: list files: %w", err)
	}
	type ref struct{ id, job string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.job); err != nil {
			rows.Close()
			return fmt.Errorf("companydeletion: scan file: %w", err)
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("companydeletion: list files: %w", err)
	}
	if len(refs) == 0 {
		return nil
	}
	if h.Storage == nil {
		return fmt.Errorf("companydeletion: %d files to delete in %q but no storage is configured", len(refs), p.Region)
	}
	st, err := h.Storage(p.Region)
	if err != nil {
		return fmt.Errorf("companydeletion: storage for %q: %w", p.Region, err)
	}
	for _, r := range refs {
		for _, key := range []string{uploads.FinalKey(p.CompanyID, r.job, r.id), uploads.StagingKey(p.CompanyID, r.job, r.id)} {
			if err = st.Delete(ctx, key); err != nil {
				return fmt.Errorf("companydeletion: delete object %s: %w", key, err)
			}
		}
	}
	return nil
}

// Subscribe starts the consumer on the bus. Stop the returned subscription on shutdown.
func Subscribe(ctx context.Context, bus events.Subscriber, router *dbrouter.DBRouter, opts ...events.SubscribeOption) (events.Subscription, error) {
	h := &Handler{
		Pools: jobmodel.RouterPools(router),
		Storage: func(reg string) (blob.BlobStore, error) {
			return router.Blob(region.Region(reg))
		},
	}
	return bus.Subscribe(ctx, Consumer, []string{SubjectFilter, ReactivatedFilter}, h.Handle, opts...)
}
