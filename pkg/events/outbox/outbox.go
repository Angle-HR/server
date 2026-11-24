// Package outbox is the transactional outbox of the event layer: Enqueue writes an event to the events_outbox
// table inside the caller's transaction, and a Relay publishes the rows to the bus afterwards.
//
// It is generic: it knows no domain and works against any one database, so each service runs its own Relay on
// its own database (a regional service runs one per region database). The table is created by
// db/migrations/000018_events_outbox_inbox.sql. The older hiring email outbox (internal/hiring/outbox) is a
// different, email specific mechanism and is left alone.
//
// Delivery is at-least-once: the relay publishes before it commits "sent", so a crash in between republishes.
// The bus drops the duplicate by event id inside its window and consumers use pkg/events/inbox beyond it.
//
// Ordering: rows are published in insertion order. A row that fails blocks the later rows with the same
// partition key (in this batch and, while it waits for its retry, in later ones). Strict per-key order assumes
// one active Relay per database; several relays remain safe (SKIP LOCKED) but may publish a key out of order.
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/pkg/events"
)

// Execer is what Enqueue needs; pgx.Tx, *pgxpool.Pool and *pgx.Conn satisfy it.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Enqueue stores the event in the outbox using the caller's transaction, so it is published if and only if the
// transaction commits. The trace context of ctx is captured now, because the relay runs later. Enqueueing an
// event id that is already there does nothing.
func Enqueue(ctx context.Context, tx Execer, ev events.Event) error {
	ev = events.InjectTrace(ctx, ev)
	raw, err := events.Encode(ev)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO events_outbox (id, type, partition_key, envelope) VALUES ($1, $2, $3, $4::jsonb)
		 ON CONFLICT (id) DO NOTHING`,
		ev.ID, ev.Type, ev.PartitionKey, string(raw))
	if err != nil {
		return fmt.Errorf("outbox: enqueue %s: %w", ev.Type, err)
	}
	return nil
}

// Purge deletes rows that were published before cutoff and returns how many. Run it from a retention task.
func Purge(ctx context.Context, db Execer, cutoff time.Time) (int64, error) {
	tag, err := db.Exec(ctx, `DELETE FROM events_outbox WHERE sent_at IS NOT NULL AND sent_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("outbox: purge: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DB starts the relay's transactions; *pgxpool.Pool satisfies it.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Result counts what a run did.
type Result struct{ Published, Retry, Dead int }

// Info describes one publish attempt for the hooks.
type Info struct {
	ID, Type string
	Attempt  int           // 1 for the first try
	Err      error         // nil on success
	Dead     bool          // the row was given up on
	Lag      time.Duration // time since the row was enqueued
	Duration time.Duration // time spent publishing
}

// Hooks let the service export metrics without this package choosing a metrics library. All are optional.
type Hooks struct {
	OnAttempt func(Info)                  // after every publish attempt, success or failure
	OnBatch   func(Result, time.Duration) // after every RunOnce
}

// Defaults.
const (
	DefaultBatchSize    = 100
	DefaultPollInterval = time.Second
	DefaultMaxAttempts  = 20
	publishTimeout      = 10 * time.Second
)

// DefaultBackoff is the retry delay after the 1st, 2nd... failure; the last repeats.
var DefaultBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
	time.Hour}

// Config configures a Relay.
type Config struct {
	DB           DB
	Publisher    events.Publisher
	BatchSize    int             // default 100
	PollInterval time.Duration   // default 1s
	MaxAttempts  int             // default 20; then the row gets failed_at and is left for an operator
	Backoff      []time.Duration // default DefaultBackoff
	Hooks        Hooks
	Logger       *slog.Logger
	Now          func() time.Time
}

// Relay moves outbox rows to the bus.
type Relay struct{ cfg Config }

// NewRelay validates and defaults the config.
func NewRelay(cfg Config) (*Relay, error) {
	if cfg.DB == nil || cfg.Publisher == nil {
		return nil, errors.New("outbox: DB and Publisher are required")
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if len(cfg.Backoff) == 0 {
		cfg.Backoff = DefaultBackoff
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Relay{cfg: cfg}, nil
}

func (r *Relay) delay(attempt int) time.Duration {
	i := attempt - 1
	if i >= len(r.cfg.Backoff) {
		i = len(r.cfg.Backoff) - 1
	}
	return r.cfg.Backoff[max(i, 0)]
}

const selectDue = `
SELECT o.seq, o.id, o.partition_key, o.envelope::text, o.attempts, o.created_at
FROM events_outbox o
WHERE o.sent_at IS NULL AND o.failed_at IS NULL AND o.next_attempt_at <= $1
  AND (o.partition_key = '' OR NOT EXISTS (
        SELECT 1 FROM events_outbox e
        WHERE e.partition_key = o.partition_key AND e.seq < o.seq
          AND e.sent_at IS NULL AND e.failed_at IS NULL AND e.next_attempt_at > $1))
ORDER BY o.seq
LIMIT $2
FOR UPDATE OF o SKIP LOCKED`

type row struct {
	seq       int64
	id, key   string
	envelope  string
	attempts  int
	createdAt time.Time
}

// RunOnce publishes one batch of due rows and returns what it did. Rows stay locked until the batch's
// transaction commits, so a second relay skips them.
func (r *Relay) RunOnce(ctx context.Context) (res Result, err error) {
	start := r.cfg.Now()
	defer func() {
		if r.cfg.Hooks.OnBatch != nil {
			r.cfg.Hooks.OnBatch(res, r.cfg.Now().Sub(start))
		}
	}()
	tx, err := r.cfg.DB.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("outbox: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	rows, err := tx.Query(ctx, selectDue, start, r.cfg.BatchSize)
	if err != nil {
		return res, fmt.Errorf("outbox: select due: %w", err)
	}
	var batch []row
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.seq, &x.id, &x.key, &x.envelope, &x.attempts, &x.createdAt); err != nil {
			rows.Close()
			return res, fmt.Errorf("outbox: scan: %w", err)
		}
		batch = append(batch, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("outbox: select due: %w", err)
	}

	blocked := map[string]bool{} // keys with a failure in this batch
	for _, x := range batch {
		if x.key != "" && blocked[x.key] {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		failed, err := r.publishOne(ctx, tx, x, &res)
		if err != nil {
			return res, err
		}
		if failed && x.key != "" {
			blocked[x.key] = true
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("outbox: commit: %w", err)
	}
	return res, nil
}

// publishOne publishes a row and records the outcome in the same transaction. It reports whether the publish
// failed; the error is for database problems only.
func (r *Relay) publishOne(ctx context.Context, tx pgx.Tx, x row, res *Result) (failed bool, err error) {
	info := Info{ID: x.id, Attempt: x.attempts + 1, Lag: r.cfg.Now().Sub(x.createdAt)}
	ev, perr := events.Decode([]byte(x.envelope))
	started := r.cfg.Now()
	if perr == nil {
		pctx, cancel := context.WithTimeout(ctx, publishTimeout)
		perr = r.cfg.Publisher.Publish(pctx, ev)
		cancel()
	}
	info.Type, info.Duration, info.Err = ev.Type, r.cfg.Now().Sub(started), perr
	defer func() {
		if r.cfg.Hooks.OnAttempt != nil {
			r.cfg.Hooks.OnAttempt(info)
		}
	}()
	if perr == nil {
		if _, err := tx.Exec(ctx, `UPDATE events_outbox SET sent_at = $2, attempts = attempts + 1, last_error = NULL
			WHERE seq = $1`, x.seq, r.cfg.Now()); err != nil {
			return false, fmt.Errorf("outbox: mark sent: %w", err)
		}
		res.Published++
		return false, nil
	}
	if ctx.Err() != nil {
		return true, nil // shutting down: not the row's fault, leave it untouched
	}
	msg := perr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	if info.Attempt >= r.cfg.MaxAttempts {
		info.Dead = true
		res.Dead++
		r.cfg.Logger.Error("outbox: giving up on event", "id", x.id, "type", ev.Type, "attempts", info.Attempt,
			"error", perr)
		_, err = tx.Exec(ctx, `UPDATE events_outbox SET failed_at = $2, attempts = attempts + 1, last_error = $3
			WHERE seq = $1`, x.seq, r.cfg.Now(), msg)
	} else {
		res.Retry++
		r.cfg.Logger.Warn("outbox: publish failed, will retry", "id", x.id, "type", ev.Type,
			"attempt", info.Attempt, "error", perr)
		_, err = tx.Exec(ctx, `UPDATE events_outbox SET next_attempt_at = $2, attempts = attempts + 1, last_error = $3
			WHERE seq = $1`, x.seq, r.cfg.Now().Add(r.delay(info.Attempt)), msg)
	}
	if err != nil {
		return true, fmt.Errorf("outbox: mark failed: %w", err)
	}
	return true, nil
}

// Run polls until ctx ends. It sleeps PollInterval after an empty or failed run and loops straight away after a
// full batch. Database errors are logged and retried, not returned.
func (r *Relay) Run(ctx context.Context) error {
	for {
		res, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.cfg.Logger.Error("outbox: relay run failed", "error", err)
		}
		if err == nil && res.Published+res.Retry+res.Dead >= r.cfg.BatchSize {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.cfg.PollInterval):
		}
	}
}
