// Package inbox makes an event handler idempotent. Handle records (consumer, event id) in events_inbox in the
// same database transaction as the handler's own writes, so either both commit or neither does, and a redelivered
// event finds its row and is acknowledged without running the handler again.
//
// The table is created by db/migrations/000018_events_outbox_inbox.sql.
package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/pkg/events"
)

// DB starts transactions; *pgxpool.Pool satisfies it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Func is the business logic. It must do all its database writes through tx and return an error to roll back
// (and have the event retried). Side effects outside the database are not covered by the inbox.
type Func func(ctx context.Context, tx pgx.Tx, e events.Event) error

// Handle wraps fn as an events.Handler with exactly-once-effect semantics for the database writes.
//
// A concurrent duplicate (two replicas receiving the same event) blocks on the primary key until the first
// transaction ends, then sees the conflict and skips; if the first rolled back, the second runs.
func Handle(db DB, consumer string, fn Func) events.Handler {
	return func(ctx context.Context, e events.Event) error {
		tx, err := db.Begin(ctx)
		if err != nil {
			return fmt.Errorf("inbox: begin: %w", err)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

		tag, err := tx.Exec(ctx,
			`INSERT INTO events_inbox (consumer, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, consumer, e.ID)
		if err != nil {
			return fmt.Errorf("inbox: record %s: %w", e.ID, err)
		}
		if tag.RowsAffected() == 0 {
			return nil // already processed: acknowledge
		}
		if err := fn(ctx, tx, e); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("inbox: commit: %w", err)
		}
		return nil
	}
}

// Seen reports whether the consumer has processed the event.
func Seen(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, consumer, eventID string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `SELECT 1 FROM events_inbox WHERE consumer = $1 AND event_id = $2`, consumer, eventID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Purge deletes rows older than cutoff. Keep them at least as long as the bus can redeliver (stream max age),
// otherwise an old redelivery would run twice.
func Purge(ctx context.Context, db interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}, cutoff time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM events_inbox WHERE processed_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("inbox: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}
