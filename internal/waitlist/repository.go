package waitlist

import (
	"context"
	"fmt"

	"github.com/Angle-HR/server/internal/db/sqlc"
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
	queries *sqlc.Queries
}

// NewPostgresWaitlistRepository returns a PostgreSQL-backed waitlist repository.
func NewPostgresWaitlistRepository(queries *sqlc.Queries) *PostgresWaitlistRepository {
	return &PostgresWaitlistRepository{queries: queries}
}

// Insert stores a waitlist entry.
//
//nolint:gocritic // Repository API passes the entry by value by design.
func (r *PostgresWaitlistRepository) Insert(ctx context.Context, entry WaitlistEntry) error {
	companyName := entry.CompanyName
	companySize := entry.CompanySize
	role := entry.Role

	err := r.queries.InsertWaitlistEntry(ctx, sqlc.InsertWaitlistEntryParams{
		Email:       entry.Email,
		CompanyName: &companyName,
		CompanySize: &companySize,
		Role:        &role,
	})
	if err != nil {
		return db.Wrap(err, "insert waitlist entry")
	}

	return nil
}

// ExistsByEmail reports whether the email is already registered.
func (r *PostgresWaitlistRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	exists, err := r.queries.WaitlistEmailExists(ctx, email)
	if err != nil {
		return false, fmt.Errorf("check waitlist email: %w", err)
	}

	return exists, nil
}

// Count returns the total number of waitlist signups.
func (r *PostgresWaitlistRepository) Count(ctx context.Context) (int64, error) {
	count, err := r.queries.CountWaitlistEntries(ctx)
	if err != nil {
		return 0, fmt.Errorf("count waitlist entries: %w", err)
	}

	return count, nil
}
