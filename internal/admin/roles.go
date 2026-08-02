package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/pkg/apperror"
)

// ListPermissions returns the seeded permission catalog.
func (s *Store) ListPermissions(ctx context.Context) ([]Permission, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT id, slug, description
		FROM admin.permissions
		ORDER BY slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Permission
	for rows.Next() {
		var p Permission
		if err := rows.Scan(&p.ID, &p.Slug, &p.Description); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if out == nil {
		out = []Permission{}
	}
	return out, rows.Err()
}

// ListRolesWithPermissions returns all roles and their permissions.
func (s *Store) ListRolesWithPermissions(ctx context.Context) ([]RoleWithPermissions, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT r.id, r.slug, r.name, r.is_system,
			COALESCE(array_agg(p.slug ORDER BY p.slug) FILTER (WHERE p.slug IS NOT NULL), '{}')
		FROM admin.roles r
		LEFT JOIN admin.role_permissions rp ON rp.role_id = r.id
		LEFT JOIN admin.permissions p ON p.id = rp.permission_id
		GROUP BY r.id
		ORDER BY r.slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoleWithPermissions
	for rows.Next() {
		var item RoleWithPermissions
		if err := rows.Scan(&item.ID, &item.Slug, &item.Name, &item.IsSystem, &item.Permissions); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if out == nil {
		out = []RoleWithPermissions{}
	}
	return out, rows.Err()
}

func (s *Store) getRoleWithPermissionsTx(ctx context.Context, q interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, id uuid.UUID) (RoleWithPermissions, error) {
	var item RoleWithPermissions
	err := q.QueryRow(ctx, `
		SELECT r.id, r.slug, r.name, r.is_system,
			COALESCE(array_agg(p.slug ORDER BY p.slug) FILTER (WHERE p.slug IS NOT NULL), '{}')
		FROM admin.roles r
		LEFT JOIN admin.role_permissions rp ON rp.role_id = r.id
		LEFT JOIN admin.permissions p ON p.id = rp.permission_id
		WHERE r.id = $1
		GROUP BY r.id
	`, id).Scan(&item.ID, &item.Slug, &item.Name, &item.IsSystem, &item.Permissions)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleWithPermissions{}, apperror.ErrNotFound
	}
	return item, err
}

func (s *Store) resolvePermissionIDs(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}, slugs []string) ([]uuid.UUID, error) {
	if len(slugs) == 0 {
		return []uuid.UUID{}, nil
	}
	rows, err := q.Query(ctx, `
		SELECT id, slug FROM admin.permissions WHERE slug = ANY($1)
	`, slugs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make(map[string]uuid.UUID, len(slugs))
	for rows.Next() {
		var id uuid.UUID
		var slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		found[slug] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ids := make([]uuid.UUID, 0, len(slugs))
	var missing []string
	seen := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		id, ok := found[slug]
		if !ok {
			missing = append(missing, slug)
			continue
		}
		ids = append(ids, id)
	}
	if len(missing) > 0 {
		return nil, apperror.NewWithDetails(
			apperror.CodeValidationError,
			"unknown permission",
			map[string]any{"permissions": missing},
		)
	}
	return ids, nil
}

func setRolePermissions(ctx context.Context, tx pgx.Tx, roleID uuid.UUID, permIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM admin.role_permissions WHERE role_id = $1`, roleID); err != nil {
		return err
	}
	for _, pid := range permIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin.role_permissions (role_id, permission_id) VALUES ($1, $2)
		`, roleID, pid); err != nil {
			return err
		}
	}
	return nil
}

// CreateRole creates a custom role with the given permissions.
func (s *Store) CreateRole(ctx context.Context, slug, name string, permissionSlugs []string) (RoleWithPermissions, error) {
	slug = strings.TrimSpace(strings.ToLower(slug))
	name = strings.TrimSpace(name)
	if slug == "" || name == "" {
		return RoleWithPermissions{}, apperror.New(apperror.CodeValidationError, "slug and name are required")
	}

	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return RoleWithPermissions{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	permIDs, err := s.resolvePermissionIDs(ctx, tx, permissionSlugs)
	if err != nil {
		return RoleWithPermissions{}, err
	}

	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO admin.roles (slug, name, is_system)
		VALUES ($1, $2, FALSE)
		RETURNING id
	`, slug, name).Scan(&id)
	if isUniqueViolation(err) {
		return RoleWithPermissions{}, apperror.New(apperror.CodeConflict, "role slug already exists")
	}
	if err != nil {
		return RoleWithPermissions{}, fmt.Errorf("create role: %w", err)
	}

	if err := setRolePermissions(ctx, tx, id, permIDs); err != nil {
		return RoleWithPermissions{}, err
	}

	item, err := s.getRoleWithPermissionsTx(ctx, tx, id)
	if err != nil {
		return RoleWithPermissions{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RoleWithPermissions{}, err
	}
	return item, nil
}

// UpdateRole patches a role name and/or replaces its permissions.
func (s *Store) UpdateRole(ctx context.Context, id uuid.UUID, name *string, permissionSlugs *[]string) (RoleWithPermissions, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return RoleWithPermissions{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var slug string
	var isSystem bool
	err = tx.QueryRow(ctx, `SELECT slug, is_system FROM admin.roles WHERE id = $1`, id).Scan(&slug, &isSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return RoleWithPermissions{}, apperror.ErrNotFound
	}
	if err != nil {
		return RoleWithPermissions{}, err
	}

	if name != nil {
		trimmed := strings.TrimSpace(*name)
		if trimmed == "" {
			return RoleWithPermissions{}, apperror.New(apperror.CodeValidationError, "name cannot be empty")
		}
		if _, err := tx.Exec(ctx, `UPDATE admin.roles SET name = $2 WHERE id = $1`, id, trimmed); err != nil {
			return RoleWithPermissions{}, err
		}
	}

	if permissionSlugs != nil {
		permIDs, err := s.resolvePermissionIDs(ctx, tx, *permissionSlugs)
		if err != nil {
			return RoleWithPermissions{}, err
		}
		if err := setRolePermissions(ctx, tx, id, permIDs); err != nil {
			return RoleWithPermissions{}, err
		}
		// If editing superadmin permissions, ensure at least one active user still has admins:write.
		if slug == RoleSuperadmin {
			var hasWrite bool
			for _, p := range *permissionSlugs {
				if p == PermAdminsWrite {
					hasWrite = true
					break
				}
			}
			if !hasWrite {
				return RoleWithPermissions{}, apperror.New(
					apperror.CodeConflict,
					"superadmin role must retain admins:write",
				)
			}
		}
	}

	item, err := s.getRoleWithPermissionsTx(ctx, tx, id)
	if err != nil {
		return RoleWithPermissions{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RoleWithPermissions{}, err
	}
	return item, nil
}

// DeleteRole deletes a non-system role that is not assigned to any user.
func (s *Store) DeleteRole(ctx context.Context, id uuid.UUID) error {
	var isSystem bool
	err := s.DB.QueryRow(ctx, `SELECT is_system FROM admin.roles WHERE id = $1`, id).Scan(&isSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperror.ErrNotFound
	}
	if err != nil {
		return err
	}
	if isSystem {
		return apperror.New(apperror.CodeConflict, "cannot delete system role")
	}

	var assigned int
	if err := s.DB.QueryRow(ctx, `SELECT COUNT(*) FROM admin.user_roles WHERE role_id = $1`, id).Scan(&assigned); err != nil {
		return err
	}
	if assigned > 0 {
		return apperror.New(apperror.CodeConflict, "role is still assigned to staff")
	}

	tag, err := s.DB.Exec(ctx, `DELETE FROM admin.roles WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return apperror.ErrNotFound
	}
	return nil
}
