package regionsaga

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/region"
)

// DB is the part of a pgx pool the stores need.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// PGStore keeps sagas in accounts.region_move_sagas of the global database.
type PGStore struct{ DB DB }

var _ Store = (*PGStore)(nil)

const sagaCols = `id, user_id, target_region, state, attempts, last_error, updated_at, completed_at`

func scanSaga(row pgx.Row) (Saga, error) {
	var s Saga
	var target string
	err := row.Scan(&s.ID, &s.UserID, &target, &s.State, &s.Attempts, &s.LastError, &s.UpdatedAt, &s.CompletedAt)
	s.Target = region.Region(target)
	return s, err
}

// Begin implements Store.
func (p *PGStore) Begin(ctx context.Context, userID uuid.UUID, target region.Region) (Saga, error) {
	for range 3 {
		s, err := p.begin(ctx, userID, target)
		if errors.Is(err, errRetryBegin) {
			continue
		}
		return s, err
	}
	return Saga{}, errors.New("regionsaga: could not start the move")
}

var errRetryBegin = errors.New("retry")

func (p *PGStore) begin(ctx context.Context, userID uuid.UUID, target region.Region) (Saga, error) {
	live, err := scanSaga(p.DB.QueryRow(ctx, `SELECT `+sagaCols+` FROM accounts.region_move_sagas
		WHERE user_id = $1 AND state NOT IN ('completed','compensated')`, userID))
	switch {
	case err == nil:
		if live.Target != target {
			return Saga{}, fmt.Errorf("%w (%s)", ErrConflict, live.Target)
		}
		return live, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Saga{}, fmt.Errorf("regionsaga: read live saga: %w", err)
	}
	done, err := scanSaga(p.DB.QueryRow(ctx, `SELECT `+sagaCols+` FROM accounts.region_move_sagas
		WHERE user_id = $1 AND state = 'completed' ORDER BY completed_at DESC LIMIT 1`, userID))
	switch {
	case err == nil:
		if done.Target != target {
			return Saga{}, fmt.Errorf("%w: it already moved to %s", ErrConflict, done.Target)
		}
		return done, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Saga{}, fmt.Errorf("regionsaga: read completed saga: %w", err)
	}
	s, err := scanSaga(p.DB.QueryRow(ctx, `INSERT INTO accounts.region_move_sagas (id, user_id, target_region)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING RETURNING `+sagaCols, uuid.New(), userID, string(target)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Saga{}, errRetryBegin // someone else inserted the live row first
	}
	if err != nil {
		return Saga{}, fmt.Errorf("regionsaga: start saga: %w", err)
	}
	return s, nil
}

// Claim implements Store.
func (p *PGStore) Claim(ctx context.Context, id uuid.UUID, lease time.Duration) (Saga, bool, error) {
	s, err := scanSaga(p.DB.QueryRow(ctx, `UPDATE accounts.region_move_sagas
		SET lease_until = now() + $2::interval, attempts = attempts + 1, updated_at = now()
		WHERE id = $1 AND state NOT IN ('completed','compensated') AND (lease_until IS NULL OR lease_until < now())
		RETURNING `+sagaCols, id, fmt.Sprintf("%d milliseconds", lease.Milliseconds())))
	if errors.Is(err, pgx.ErrNoRows) {
		return Saga{}, false, nil
	}
	if err != nil {
		return Saga{}, false, fmt.Errorf("regionsaga: claim: %w", err)
	}
	return s, true, nil
}

// Advance implements Store.
func (p *PGStore) Advance(ctx context.Context, id uuid.UUID, from, to string) error {
	tag, err := p.DB.Exec(ctx, `UPDATE accounts.region_move_sagas
		SET state = $3, last_error = CASE WHEN $3 IN ('completed') THEN '' ELSE last_error END, updated_at = now(),
		    completed_at = CASE WHEN $3 = 'completed' THEN now() ELSE completed_at END
		WHERE id = $1 AND state = $2`, id, from, to)
	if err != nil {
		return fmt.Errorf("regionsaga: advance %s to %s: %w", from, to, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("regionsaga: saga %s is no longer in state %s", id, from)
	}
	return nil
}

// Fail implements Store.
func (p *PGStore) Fail(ctx context.Context, id uuid.UUID, msg string) error {
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, err := p.DB.Exec(ctx, `UPDATE accounts.region_move_sagas SET last_error = $2, updated_at = now() WHERE id = $1`, id, msg)
	return err
}

// Release implements Store.
func (p *PGStore) Release(ctx context.Context, id uuid.UUID) error {
	_, err := p.DB.Exec(ctx, `UPDATE accounts.region_move_sagas SET lease_until = NULL WHERE id = $1`, id)
	return err
}

// Get implements Store.
func (p *PGStore) Get(ctx context.Context, id uuid.UUID) (Saga, error) {
	return scanSaga(p.DB.QueryRow(ctx, `SELECT `+sagaCols+` FROM accounts.region_move_sagas WHERE id = $1`, id))
}

// Resumable implements Store.
func (p *PGStore) Resumable(ctx context.Context, cutoff time.Time, limit int) ([]Saga, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+sagaCols+` FROM accounts.region_move_sagas
		WHERE state NOT IN ('completed','compensated') AND updated_at < $1 AND (lease_until IS NULL OR lease_until < now())
		ORDER BY updated_at LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("regionsaga: list resumable: %w", err)
	}
	defer rows.Close()
	var out []Saga
	for rows.Next() {
		s, serr := scanSaga(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
