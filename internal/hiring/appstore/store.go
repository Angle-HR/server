// Package appstore is the PostgreSQL implementation of the applications side: applications and their files,
// review tasks, reapply blocks, the email outbox, the per-job applicant counters and the company's candidate
// settings (reapply_block_months and send_status_emails of hiring.org_settings).
//
// It reads and writes only tables the applications service owns. Everything it needs to know about a job or a
// company comes from an appjobs.RegionalJobs, never from a join into hiring or accounts tables. The
// audit and compliance rows go to its own hiring.app_audit_events and hiring.app_compliance_log (audit.go), written
// in the same transaction as the change they record.
//
// Like hiringstore, regional tables have row-level security that shows nothing without app.tenant_id, so every
// call runs in a transaction scoped to one company.
package appstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// DB is the subset of a pgx pool the store needs.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Errors.
var (
	ErrNotFound = apperror.ErrNotFound
	// ErrConflict means a row that must be unique already exists, or was changed by a concurrent request.
	ErrConflict = errors.New("appstore: already exists")
)

// Postgres error codes the store translates.
const pgUniqueViolation = "23505"

// mapPgError turns a unique violation into ErrConflict.
func mapPgError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return ErrConflict
	}
	return err
}

// Store persists one region's application data.
type Store struct {
	DB DB
	// Jobs supplies the frozen form questions when an application is opened.
	Jobs appjobs.RegionalJobs
	// Region is the region this store's database serves. It stamps the application events written to the
	// region's outbox (APPLICATIONS_EVENTS=on, see package appevents); it may be empty while events are off.
	Region string
}

// inTenantTx runs fn in a transaction scoped to one company. The tenant id is set for the transaction only
// (set_config with is_local = true), so a pooled connection never carries it into the next request.
func (s *Store) inTenantTx(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) (err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("appstore: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			besteffort.Log(ctx, "appstore.rollback", tx.Rollback(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return fmt.Errorf("appstore: set tenant: %w", err)
	}
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("appstore: commit: %w", err)
	}
	committed = true
	return nil
}
