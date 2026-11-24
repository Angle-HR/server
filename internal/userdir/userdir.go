// Package userdir is the identity service's view of product users: the global registry (auth.users_registry,
// email and user id -> region) plus the regional accounts.users row. It backs the gRPC user lookups, the admin user
// list and the activate/deactivate switch, so the HTTP and gRPC surfaces share one implementation.
package userdir

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

// DB is a database as the directory needs it.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// User is the display data of one product user.
type User struct {
	ID            string
	Email         string
	FirstName     string
	LastName      string
	LegalFullName string
	AccountType   string
	Region        string
	EmailVerified bool
	Active        bool
	CreatedAt     time.Time
}

// Directory reads and changes users across the registry and the regional databases.
type Directory struct {
	Global DB
	Router *dbrouter.DBRouter
}

// ErrNotFound means the registry has no such user.
var ErrNotFound = errors.New("userdir: user not found")

func (d *Directory) pool(reg region.Region) (DB, error) {
	if reg == region.RegionGlobal {
		return d.Global, nil
	}
	p, err := d.Router.DB(reg)
	if err != nil {
		return nil, err
	}
	return p, nil
}

type registryRow struct {
	userID    uuid.UUID
	region    string
	createdAt time.Time
}

// registry finds the registry row by user id or by registry id.
func (d *Directory) registry(ctx context.Context, id uuid.UUID) (registryRow, error) {
	var r registryRow
	err := d.Global.QueryRow(ctx, `
		SELECT user_id, region, created_at FROM auth.users_registry
		WHERE (user_id = $1 OR id = $1) AND user_id IS NOT NULL
		LIMIT 1`, id).Scan(&r.userID, &r.region, &r.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// userSelect reads the user columns in the order scan expects. Accounts still in the global holding area live in
// accounts.pending_users, which has fewer columns.
func userSelect(reg string) string {
	if reg == string(region.RegionGlobal) {
		return `SELECT id::text, email, '', '', COALESCE(legal_full_name,''), COALESCE(account_type,''),
	       email_verified_at IS NOT NULL, deleted_at IS NULL, created_at FROM accounts.pending_users`
	}
	return `SELECT id::text, email, COALESCE(first_name,''), COALESCE(last_name,''), COALESCE(legal_full_name,''),
	       COALESCE(account_type,''), email_verified_at IS NOT NULL, deleted_at IS NULL, created_at
	FROM accounts.users`
}

func usersTable(reg region.Region) string {
	if reg == region.RegionGlobal {
		return "accounts.pending_users"
	}
	return "accounts.users"
}

func scan(row pgx.Row, reg string) (User, error) {
	u := User{Region: reg}
	err := row.Scan(&u.ID, &u.Email, &u.FirstName, &u.LastName, &u.LegalFullName, &u.AccountType,
		&u.EmailVerified, &u.Active, &u.CreatedAt)
	return u, err
}

// Get returns one user by user id (or registry id).
func (d *Directory) Get(ctx context.Context, id uuid.UUID) (User, error) {
	rr, err := d.registry(ctx, id)
	if err != nil {
		return User{}, err
	}
	reg, err := region.ParseRegion(rr.region)
	if err != nil {
		return User{}, err
	}
	p, err := d.pool(reg)
	if err != nil {
		return User{}, err
	}
	u, err := scan(p.QueryRow(ctx, userSelect(rr.region)+` WHERE id = $1`, rr.userID), rr.region)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// Batch resolves many users. Unknown ids are skipped. excludeInactive drops deactivated users. Lookups are
// grouped by region, one query per region.
func (d *Directory) Batch(ctx context.Context, ids []uuid.UUID, excludeInactive bool) ([]User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.Global.Query(ctx, `
		SELECT user_id, region FROM auth.users_registry
		WHERE user_id = ANY($1) OR id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("userdir: registry lookup: %w", err)
	}
	byRegion := map[string][]uuid.UUID{}
	for rows.Next() {
		var uid *uuid.UUID
		var reg string
		if err := rows.Scan(&uid, &reg); err != nil {
			rows.Close()
			return nil, err
		}
		if uid != nil {
			byRegion[reg] = append(byRegion[reg], *uid)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []User
	for reg, uids := range byRegion {
		r, perr := region.ParseRegion(reg)
		if perr != nil {
			continue
		}
		p, perr := d.pool(r)
		if perr != nil {
			return nil, perr
		}
		q := userSelect(reg) + ` WHERE id = ANY($1)`
		if excludeInactive {
			q += ` AND deleted_at IS NULL`
		}
		urows, qerr := p.Query(ctx, q, uids)
		if qerr != nil {
			return nil, fmt.Errorf("userdir: regional lookup: %w", qerr)
		}
		for urows.Next() {
			u, serr := scan(urows, reg)
			if serr != nil {
				urows.Close()
				return nil, serr
			}
			out = append(out, u)
		}
		urows.Close()
		if err := urows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RegistryEntry is one row of the registry as the admin list shows it.
type RegistryEntry struct {
	RegistryID uuid.UUID
	Email      string
	Region     string
	UserID     uuid.UUID
	CreatedAt  time.Time
}

// List pages the registry newest first. query matches the email, region keeps one region.
func (d *Directory) List(ctx context.Context, query, regionFilter string, limit, offset int) ([]RegistryEntry, error) {
	rows, err := d.Global.Query(ctx, `
		SELECT id, email, region, user_id, created_at
		FROM auth.users_registry
		WHERE user_id IS NOT NULL
		  AND ($1 = '' OR email ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR region = $2)
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4`, query, regionFilter, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RegistryEntry{}
	for rows.Next() {
		var e RegistryEntry
		if err := rows.Scan(&e.RegistryID, &e.Email, &e.Region, &e.UserID, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LookupRegion answers which region holds the user, by email or by user id.
func (d *Directory) LookupRegion(ctx context.Context, email string, userID uuid.UUID) (uuid.UUID, string, bool, error) {
	var uid *uuid.UUID
	var reg string
	var err error
	if email != "" {
		err = d.Global.QueryRow(ctx, `SELECT user_id, region FROM auth.users_registry WHERE lower(email) = lower($1)
			AND user_id IS NOT NULL`, email).Scan(&uid, &reg)
	} else {
		err = d.Global.QueryRow(ctx, `SELECT user_id, region FROM auth.users_registry WHERE user_id = $1`, userID).
			Scan(&uid, &reg)
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && uid == nil) {
		return uuid.Nil, "", false, nil
	}
	if err != nil {
		return uuid.Nil, "", false, err
	}
	return *uid, reg, true, nil
}

// SetActive deactivates (soft-deletes) or reactivates the account. Deactivating also writes the
// identity.user.deactivated.v1 event in the same transaction when IDENTITY_EVENTS=on. byAdmin is the acting
// operator, empty when unknown. The returned user reflects the new state.
func (d *Directory) SetActive(ctx context.Context, id uuid.UUID, active bool, byAdmin string) (User, error) {
	rr, err := d.registry(ctx, id)
	if err != nil {
		return User{}, err
	}
	reg, err := region.ParseRegion(rr.region)
	if err != nil {
		return User{}, err
	}
	p, err := d.pool(reg)
	if err != nil {
		return User{}, err
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if active {
		_, err = tx.Exec(ctx, `UPDATE `+usersTable(reg)+` SET deleted_at = NULL, updated_at = now() WHERE id = $1`, rr.userID)
	} else {
		var was bool
		err = tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM `+usersTable(reg)+` WHERE id = $1 FOR UPDATE`, rr.userID).Scan(&was)
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE `+usersTable(reg)+` SET deleted_at = COALESCE(deleted_at, now()), updated_at = now()
				WHERE id = $1`, rr.userID)
		}
		if err == nil && was {
			err = identityevents.EnqueueDeactivated(ctx, tx, identityevents.UserDeactivated{
				UserID: rr.userID.String(), Region: rr.region, ByAdminID: byAdmin, DeactivatedAt: time.Now(),
			})
		}
	}
	if err != nil {
		return User{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return d.Get(ctx, rr.userID)
}

// ToAppError maps ErrNotFound to the API's 404.
func ToAppError(err error) error {
	if errors.Is(err, ErrNotFound) {
		return apperror.ErrNotFound
	}
	return err
}
