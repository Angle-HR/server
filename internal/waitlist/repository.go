package waitlist

import (
	"context"
	"fmt"

	qb "github.com/Software78/sql-go-query-builder"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/pkg/db"
)

// WaitlistRepository persists waitlist signups.
//
//nolint:revive // Public API name matches the waitlist domain vocabulary.
type WaitlistRepository interface {
	Insert(ctx context.Context, entry WaitlistEntry) error
	ExistsByEmail(ctx context.Context, email string) (bool, error)
	Count(ctx context.Context) (int64, error)
}

// PostgresWaitlistRepository stores waitlist entries in PostgreSQL.
type PostgresWaitlistRepository struct {
	pool *pgxpool.Pool
	qb   *qb.QB
}

// NewPostgresWaitlistRepository returns a PostgreSQL-backed waitlist repository.
func NewPostgresWaitlistRepository(pool *pgxpool.Pool) *PostgresWaitlistRepository {
	return &PostgresWaitlistRepository{pool: pool, qb: qb.NewPostgres()}
}

// Insert stores a waitlist entry.
//
//nolint:gocritic // Repository API passes the entry by value by design.
func (r *PostgresWaitlistRepository) Insert(ctx context.Context, entry WaitlistEntry) error {
	sql, args, err := r.qb.Insert("waitlist").
		Columns("email", "company_name", "company_size", "role").
		Values(entry.Email, entry.CompanyName, entry.CompanySize, entry.Role).
		ToSQL()
	if err != nil {
		return db.Wrap(fmt.Errorf("build insert waitlist entry: %w", err), "insert waitlist entry")
	}

	if _, execErr := r.pool.Exec(ctx, sql, args...); execErr != nil {
		return db.Wrap(execErr, "insert waitlist entry")
	}

	return nil
}

// ExistsByEmail reports whether the email is already registered.
func (r *PostgresWaitlistRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	const query = `
SELECT EXISTS (
    SELECT 1
    FROM waitlist
    WHERE email = $1
      AND deleted_at IS NULL
)`

	var exists bool
	if err := r.pool.QueryRow(ctx, query, email).Scan(&exists); err != nil {
		return false, fmt.Errorf("check waitlist email: %w", err)
	}

	return exists, nil
}

// Count returns the total number of waitlist signups.
func (r *PostgresWaitlistRepository) Count(ctx context.Context) (int64, error) {
	sql, args, err := r.qb.Select("COUNT(*)").
		From("waitlist").
		WhereNull("deleted_at").
		ToSQL()
	if err != nil {
		return 0, fmt.Errorf("build count waitlist entries: %w", err)
	}

	var count int64
	if queryErr := r.pool.QueryRow(ctx, sql, args...).Scan(&count); queryErr != nil {
		return 0, fmt.Errorf("count waitlist entries: %w", queryErr)
	}

	return count, nil
}
