package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
)

// InviteTTL is how long an admin invite token remains valid.
const InviteTTL = 72 * time.Hour

// DB is the subset of pgxpool used by the admin store.
type DB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Store reads and writes admin.* tables.
type Store struct {
	DB DB
}

// NewStore returns an admin store.
func NewStore(db DB) *Store {
	return &Store{DB: db}
}

// User is an admin principal.
type User struct {
	ID           uuid.UUID `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	IsActive     bool      `json:"is_active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	PasswordHash *string   `json:"-"`
}

// Role is an RBAC role.
type Role struct {
	ID       uuid.UUID `json:"id"`
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	IsSystem bool      `json:"is_system"`
}

// Permission is an RBAC permission.
type Permission struct {
	ID          uuid.UUID `json:"id"`
	Slug        string    `json:"slug"`
	Description string    `json:"description"`
}

// RoleWithPermissions includes permission slugs for a role.
type RoleWithPermissions struct {
	Role
	Permissions []string `json:"permissions"`
}

// StaffMember is an admin user with role slugs.
type StaffMember struct {
	ID            uuid.UUID `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	IsActive      bool      `json:"is_active"`
	InvitePending bool      `json:"invite_pending"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Roles         []string  `json:"roles"`
}

// InvitePreview is the public view of a pending invite.
type InvitePreview struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuditLog is an admin mutation record.
type AuditLog struct {
	ID           int64           `json:"id"`
	ActorID      *uuid.UUID      `json:"actor_id,omitempty"`
	ActorEmail   *string         `json:"actor_email,omitempty"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   *string         `json:"resource_id,omitempty"`
	Meta         json.RawMessage `json:"meta"`
	IP           *string         `json:"ip,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// HashInviteToken returns a hex-encoded SHA-256 digest of the raw token.
func HashInviteToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewInviteToken generates a URL-safe opaque invite token.
func NewInviteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate invite token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// CountUsers returns the number of admin users.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM admin.users`).Scan(&n)
	return n, err
}

// CreateUser inserts an admin user and returns it. passwordHash may be nil for pending invites.
func (s *Store) CreateUser(ctx context.Context, email string, passwordHash *string, name string, active bool) (User, error) {
	var u User
	err := s.DB.QueryRow(ctx, `
		INSERT INTO admin.users (email, password_hash, name, is_active)
		VALUES ($1, $2, $3, $4)
		RETURNING id, email, name, is_active, created_at, updated_at, password_hash
	`, email, passwordHash, name, active).Scan(
		&u.ID, &u.Email, &u.Name, &u.IsActive, &u.CreatedAt, &u.UpdatedAt, &u.PasswordHash,
	)
	if isUniqueViolation(err) {
		return User{}, apperror.New(apperror.CodeConflict, "admin email already registered")
	}
	if err != nil {
		return User{}, fmt.Errorf("create admin user: %w", err)
	}
	return u, nil
}

// AssignRoleBySlug assigns a role to a user by role slug.
func (s *Store) AssignRoleBySlug(ctx context.Context, userID uuid.UUID, roleSlug string) error {
	tag, err := s.DB.Exec(ctx, `
		INSERT INTO admin.user_roles (user_id, role_id)
		SELECT $1, r.id FROM admin.roles r WHERE r.slug = $2
		ON CONFLICT DO NOTHING
	`, userID, roleSlug)
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin.roles WHERE slug = $1)`, roleSlug).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return apperror.New(apperror.CodeNotFound, "role not found")
		}
	}
	return nil
}

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.IsActive, &u.CreatedAt, &u.UpdatedAt, &u.PasswordHash)
	return u, err
}

// GetUserByEmail loads an admin user by email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	u, err := scanUser(s.DB.QueryRow(ctx, `
		SELECT id, email, name, is_active, created_at, updated_at, password_hash
		FROM admin.users WHERE email = $1
	`, email))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, apperror.ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUserByID loads an admin user by id.
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (User, error) {
	u, err := scanUser(s.DB.QueryRow(ctx, `
		SELECT id, email, name, is_active, created_at, updated_at, password_hash
		FROM admin.users WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, apperror.ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// ListPermissionsForUser returns permission slugs for an admin user.
func (s *Store) ListPermissionsForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT DISTINCT p.slug
		FROM admin.user_roles ur
		JOIN admin.role_permissions rp ON rp.role_id = ur.role_id
		JOIN admin.permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = $1
		ORDER BY p.slug
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var perms []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		perms = append(perms, slug)
	}
	if perms == nil {
		perms = []string{}
	}
	return perms, rows.Err()
}

// ListRolesForUser returns role slugs for an admin user.
func (s *Store) ListRolesForUser(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT r.slug
		FROM admin.user_roles ur
		JOIN admin.roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1
		ORDER BY r.slug
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		roles = append(roles, slug)
	}
	if roles == nil {
		roles = []string{}
	}
	return roles, rows.Err()
}

const staffSelectSQL = `
	SELECT u.id, u.email, u.name, u.is_active,
		(u.password_hash IS NULL) AS invite_pending,
		u.created_at, u.updated_at,
		COALESCE(array_agg(r.slug ORDER BY r.slug) FILTER (WHERE r.slug IS NOT NULL), '{}')
	FROM admin.users u
	LEFT JOIN admin.user_roles ur ON ur.user_id = u.id
	LEFT JOIN admin.roles r ON r.id = ur.role_id
`

func scanStaffMember(row pgx.Row) (StaffMember, error) {
	var m StaffMember
	err := row.Scan(&m.ID, &m.Email, &m.Name, &m.IsActive, &m.InvitePending, &m.CreatedAt, &m.UpdatedAt, &m.Roles)
	return m, err
}

// ListStaff returns admin users with their role slugs.
func (s *Store) ListStaff(ctx context.Context) ([]StaffMember, error) {
	rows, err := s.DB.Query(ctx, staffSelectSQL+`
		GROUP BY u.id
		ORDER BY u.created_at
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StaffMember
	for rows.Next() {
		var m StaffMember
		if err := rows.Scan(&m.ID, &m.Email, &m.Name, &m.IsActive, &m.InvitePending, &m.CreatedAt, &m.UpdatedAt, &m.Roles); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if out == nil {
		out = []StaffMember{}
	}
	return out, rows.Err()
}

// GetStaffByID returns a single staff member.
func (s *Store) GetStaffByID(ctx context.Context, id uuid.UUID) (StaffMember, error) {
	m, err := scanStaffMember(s.DB.QueryRow(ctx, staffSelectSQL+`
		WHERE u.id = $1
		GROUP BY u.id
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return StaffMember{}, apperror.ErrNotFound
	}
	return m, err
}

func guardLastSuperadmin(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, userID uuid.UUID, willRemainActiveSuperadmin bool) error {
	var has bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM admin.user_roles ur
			JOIN admin.roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1 AND r.slug = $2
		)
	`, userID, RoleSuperadmin).Scan(&has)
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	if willRemainActiveSuperadmin {
		return nil
	}
	var n int
	err = q.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM admin.users u
		JOIN admin.user_roles ur ON ur.user_id = u.id
		JOIN admin.roles r ON r.id = ur.role_id
		WHERE u.is_active = TRUE AND r.slug = $1 AND u.id <> $2
	`, RoleSuperadmin, userID).Scan(&n)
	if err != nil {
		return err
	}
	if n == 0 {
		return apperror.New(apperror.CodeConflict, "cannot remove or deactivate the last active superadmin")
	}
	return nil
}

// UpdateStaff patches staff fields and optionally replaces roles.
func (s *Store) UpdateStaff(ctx context.Context, id uuid.UUID, name *string, isActive *bool, roleSlugs *[]string) (StaffMember, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return StaffMember{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var currentActive bool
	var currentHasSuperadmin bool
	err = tx.QueryRow(ctx, `
		SELECT u.is_active,
			EXISTS(
				SELECT 1 FROM admin.user_roles ur
				JOIN admin.roles r ON r.id = ur.role_id
				WHERE ur.user_id = u.id AND r.slug = $2
			)
		FROM admin.users u WHERE u.id = $1
	`, id, RoleSuperadmin).Scan(&currentActive, &currentHasSuperadmin)
	if errors.Is(err, pgx.ErrNoRows) {
		return StaffMember{}, apperror.ErrNotFound
	}
	if err != nil {
		return StaffMember{}, err
	}

	willBeActive := currentActive
	if isActive != nil {
		willBeActive = *isActive
	}
	willHaveSuperadmin := currentHasSuperadmin
	if roleSlugs != nil {
		willHaveSuperadmin = false
		for _, slug := range *roleSlugs {
			if slug == RoleSuperadmin {
				willHaveSuperadmin = true
				break
			}
		}
	}

	if err := guardLastSuperadmin(ctx, tx, id, willBeActive && willHaveSuperadmin); err != nil {
		return StaffMember{}, err
	}

	if name != nil || isActive != nil {
		_, err = tx.Exec(ctx, `
			UPDATE admin.users SET
				name = COALESCE($2, name),
				is_active = COALESCE($3, is_active),
				updated_at = now()
			WHERE id = $1
		`, id, name, isActive)
		if err != nil {
			return StaffMember{}, err
		}
	}

	if roleSlugs != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM admin.user_roles WHERE user_id = $1`, id); err != nil {
			return StaffMember{}, err
		}
		for _, slug := range *roleSlugs {
			tag, err := tx.Exec(ctx, `
				INSERT INTO admin.user_roles (user_id, role_id)
				SELECT $1, r.id FROM admin.roles r WHERE r.slug = $2
			`, id, slug)
			if err != nil {
				return StaffMember{}, err
			}
			if tag.RowsAffected() == 0 {
				return StaffMember{}, apperror.NewWithDetails(
					apperror.CodeValidationError,
					"unknown role",
					map[string]any{"role": slug},
				)
			}
		}
	}

	m, err := scanStaffMember(tx.QueryRow(ctx, staffSelectSQL+`
		WHERE u.id = $1
		GROUP BY u.id
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return StaffMember{}, apperror.ErrNotFound
	}
	if err != nil {
		return StaffMember{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return StaffMember{}, err
	}
	return m, nil
}

// WriteAudit inserts an audit log row.
func (s *Store) WriteAudit(ctx context.Context, actorID uuid.UUID, action, resourceType, resourceID string, meta map[string]any, ip string) error {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		metaJSON = []byte("{}")
	}
	var rid *string
	if resourceID != "" {
		rid = &resourceID
	}
	var ipPtr *string
	if ip != "" {
		ipPtr = &ip
	}
	_, err = s.DB.Exec(ctx, `
		INSERT INTO admin.audit_logs (actor_id, action, resource_type, resource_id, meta, ip)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, actorID, action, resourceType, rid, metaJSON, ipPtr)
	return err
}

// ListAuditLogs returns audit logs with optional filters.
func (s *Store) ListAuditLogs(ctx context.Context, actorID *uuid.UUID, action, resourceType string, limit, offset int) ([]AuditLog, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	rows, err := s.DB.Query(ctx, `
		SELECT a.id, a.actor_id, u.email, a.action, a.resource_type, a.resource_id, a.meta, a.ip, a.created_at
		FROM admin.audit_logs a
		LEFT JOIN admin.users u ON u.id = a.actor_id
		WHERE ($1::uuid IS NULL OR a.actor_id = $1)
		  AND ($2 = '' OR a.action = $2)
		  AND ($3 = '' OR a.resource_type = $3)
		ORDER BY a.created_at DESC
		LIMIT $4 OFFSET $5
	`, actorID, action, resourceType, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AuditLog
	for rows.Next() {
		var log AuditLog
		if err := rows.Scan(
			&log.ID, &log.ActorID, &log.ActorEmail, &log.Action,
			&log.ResourceType, &log.ResourceID, &log.Meta, &log.IP, &log.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	if out == nil {
		out = []AuditLog{}
	}
	return out, rows.Err()
}

// GetAdminPrincipal implements auth.AdminUserStore.
func (s *Store) GetAdminPrincipal(ctx context.Context, id uuid.UUID) (auth.AdminPrincipal, error) {
	u, err := s.GetUserByID(ctx, id)
	if err != nil {
		return auth.AdminPrincipal{}, err
	}
	return auth.AdminPrincipal{ID: u.ID, Email: u.Email, IsActive: u.IsActive}, nil
}

// ListAdminPermissions implements auth.AdminUserStore.
func (s *Store) ListAdminPermissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	return s.ListPermissionsForUser(ctx, userID)
}
