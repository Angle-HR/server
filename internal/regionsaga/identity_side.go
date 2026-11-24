package regionsaga

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/region"
)

// ErrNotPending: the user is neither in the holding area nor registered in the target region.
var ErrNotPending = errors.New("regionsaga: the account is not waiting in the global holding area")

// ErrCommitted: the move already committed, so it cannot be aborted.
var ErrCommitted = errors.New("regionsaga: the move already committed")

// IdentitySide is identity's half of the saga: it owns accounts.pending_users, auth.users_registry and the regional
// accounts.users rows. identitysvc serves it as IdentityService.PrepareRegionMove, CommitRegionMove and
// AbortRegionMove.
type IdentitySide struct {
	Global DB
	// Regional returns the database of a real region.
	Regional func(region.Region) (DB, error)
}

type pendingUser struct {
	ID              uuid.UUID
	Email           string
	PasswordHash    string
	EmailVerifiedAt *time.Time
	AccountType     *string
	LegalFullName   *string
}

func (i *IdentitySide) pending(ctx context.Context, userID uuid.UUID) (*pendingUser, error) {
	var u pendingUser
	err := i.Global.QueryRow(ctx, `SELECT id, email, password_hash, email_verified_at, account_type, legal_full_name
		FROM accounts.pending_users WHERE id = $1 AND deleted_at IS NULL`, userID).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.EmailVerifiedAt, &u.AccountType, &u.LegalFullName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // absent is a normal answer here
	}
	if err != nil {
		return nil, fmt.Errorf("identity: read pending user: %w", err)
	}
	return &u, nil
}

func (i *IdentitySide) registryRegion(ctx context.Context, userID uuid.UUID) (string, bool, error) {
	var reg string
	err := i.Global.QueryRow(ctx, `SELECT region FROM auth.users_registry WHERE user_id = $1`, userID).Scan(&reg)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return reg, err == nil, err
}

func (i *IdentitySide) regionalHas(ctx context.Context, target region.Region, userID uuid.UUID) (bool, error) {
	pool, err := i.Regional(target)
	if err != nil {
		return false, err
	}
	var one int
	err = pool.QueryRow(ctx, `SELECT 1 FROM accounts.users WHERE id = $1`, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Prepare copies the pending user into the target region (idempotent). alreadyPresent is true on a retry, or when
// the account was moved earlier.
func (i *IdentitySide) Prepare(ctx context.Context, userID uuid.UUID, target region.Region) (alreadyPresent bool, err error) {
	if !region.Valid(target) || target == region.RegionGlobal {
		return false, Permanent{fmt.Errorf("identity: %q is not a region an account can move to", target)}
	}
	pool, err := i.Regional(target)
	if err != nil {
		return false, Permanent{fmt.Errorf("identity: region %q has no database: %w", target, err)}
	}
	u, err := i.pending(ctx, userID)
	if err != nil {
		return false, err
	}
	if u == nil {
		reg, found, rerr := i.registryRegion(ctx, userID)
		if rerr != nil {
			return false, rerr
		}
		has, herr := i.regionalHas(ctx, target, userID)
		if herr != nil {
			return false, herr
		}
		if found && reg == string(target) && has {
			return true, nil
		}
		return false, Permanent{ErrNotPending}
	}
	tag, err := pool.Exec(ctx, `INSERT INTO accounts.users (id, email, password_hash, email_verified_at, account_type, legal_full_name)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		u.ID, u.Email, u.PasswordHash, u.EmailVerifiedAt, u.AccountType, u.LegalFullName)
	if err != nil {
		return false, fmt.Errorf("identity: copy user to %s: %w", target, err)
	}
	return tag.RowsAffected() == 0, nil
}

// Commit repoints users_registry at the target region and drops the pending user, in one transaction. It is the
// point of no return. alreadyCommitted is true when a previous call did it.
func (i *IdentitySide) Commit(ctx context.Context, userID uuid.UUID, target region.Region) (alreadyCommitted bool, err error) {
	if !region.Valid(target) || target == region.RegionGlobal {
		return false, Permanent{fmt.Errorf("identity: %q is not a region an account can move to", target)}
	}
	has, err := i.regionalHas(ctx, target, userID)
	if err != nil {
		return false, err
	}
	if !has {
		return false, Permanent{fmt.Errorf("identity: user %s was not prepared in %s", userID, target)}
	}
	u, err := i.pending(ctx, userID)
	if err != nil {
		return false, err
	}
	if u == nil {
		reg, found, rerr := i.registryRegion(ctx, userID)
		if rerr != nil {
			return false, rerr
		}
		if found && reg == string(target) {
			return true, nil
		}
		return false, Permanent{fmt.Errorf("identity: user %s already lives in %q, not %q", userID, reg, target)}
	}
	tx, err := i.Global.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `UPDATE auth.users_registry SET region = $3, region_source = 'explicit'
		WHERE user_id = $1 OR lower(email) = lower($2)`, userID, u.Email, string(target)); err != nil {
		return false, fmt.Errorf("identity: repoint registry: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.pending_users WHERE id = $1`, userID); err != nil {
		return false, fmt.Errorf("identity: release pending user: %w", err)
	}
	return false, tx.Commit(ctx)
}

// Abort removes the user row Prepare copied while the registry still points at the holding area (idempotent).
func (i *IdentitySide) Abort(ctx context.Context, userID uuid.UUID, target region.Region) (removed bool, err error) {
	u, err := i.pending(ctx, userID)
	if err != nil {
		return false, err
	}
	if u == nil {
		reg, found, rerr := i.registryRegion(ctx, userID)
		if rerr != nil {
			return false, rerr
		}
		if found && reg == string(target) {
			return false, Permanent{ErrCommitted}
		}
		return false, nil // never pending here, nothing was copied by this saga
	}
	pool, err := i.Regional(target)
	if err != nil {
		return false, err
	}
	tag, err := pool.Exec(ctx, `DELETE FROM accounts.users WHERE id = $1`, userID)
	if err != nil {
		return false, fmt.Errorf("identity: remove copied user from %s: %w", target, err)
	}
	return tag.RowsAffected() > 0, nil
}
