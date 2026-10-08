// Package hiringstore is the PostgreSQL implementation of job drafts, the application form, templates and the
// hiring catalogs. Regional data (jobs, forms, templates) lives in the company's own region and is reached
// through a transaction that sets app.tenant_id, because every hiring table has row-level security that
// shows nothing when no tenant is set. Global reference data (markets, catalogs) has its own Global store.
package hiringstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// DB is the subset of a pgx pool the stores need.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Errors are shared with the service layer through hiringtypes.
var (
	ErrNotFound            = hiringtypes.ErrNotFound
	ErrConflict            = hiringtypes.ErrConflict
	ErrBadReference        = hiringtypes.ErrBadReference
	ErrLawfulBasisRequired = hiringtypes.ErrLawfulBasisRequired
	ErrNotDraft            = hiringtypes.ErrNotDraft
)

// StaleRevisionError means the job changed since the caller loaded it.
type StaleRevisionError = hiringtypes.StaleRevisionError

// Store persists regional hiring data.
type Store struct{ DB DB }

// Postgres error codes the store translates.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
)

// mapPgError turns constraint violations into the store's sentinel errors.
func mapPgError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgUniqueViolation:
			return ErrConflict
		case pgForeignKeyViolation:
			return ErrBadReference
		}
	}
	return err
}

// inTenantTx runs fn in a transaction scoped to one company. The tenant id is set for the transaction only
// (set_config with is_local = true), so a pooled connection never carries it into the next request.
func (s *Store) inTenantTx(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) (err error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("hiringstore: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			besteffort.Log(ctx, "hiringstore.rollback", tx.Rollback(ctx))
		}
	}()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		return fmt.Errorf("hiringstore: set tenant: %w", err)
	}
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("hiringstore: commit: %w", err)
	}
	committed = true
	return nil
}
