package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// BootstrapConfig seeds the first superadmin when no admin users exist.
type BootstrapConfig struct {
	Email        string
	PasswordHash string
	Name         string
}

// Bootstrap creates the first superadmin when the admin.users table is empty.
// PasswordHash must already be bcrypt-hashed by the caller.
func Bootstrap(ctx context.Context, store *Store, cfg BootstrapConfig) error {
	email := strings.TrimSpace(strings.ToLower(cfg.Email))
	if email == "" || cfg.PasswordHash == "" {
		return nil
	}

	n, err := store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count admin users: %w", err)
	}
	if n > 0 {
		return nil
	}

	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "Bootstrap Admin"
	}

	hash := cfg.PasswordHash
	user, err := store.CreateUser(ctx, email, &hash, name, true)
	if err != nil {
		return err
	}

	if err := store.AssignRoleBySlug(ctx, user.ID, RoleSuperadmin); err != nil {
		return err
	}

	slog.Info("admin bootstrap user created", "email", email, "role", RoleSuperadmin)
	return nil
}
