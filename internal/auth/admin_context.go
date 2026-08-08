package auth

import (
	"context"

	"github.com/google/uuid"
)

const (
	adminUserIDKey      contextKey = "adminUserID"
	adminPermissionsKey contextKey = "adminPermissions"
	adminEmailKey       contextKey = "adminEmail"
)

// WithAdmin stores authenticated admin identity on the context.
func WithAdmin(ctx context.Context, userID uuid.UUID, email string, permissions []string) context.Context {
	ctx = context.WithValue(ctx, adminUserIDKey, userID)
	ctx = context.WithValue(ctx, adminEmailKey, email)
	return context.WithValue(ctx, adminPermissionsKey, permissions)
}

// AdminFromContext returns the authenticated admin user ID, email, and permissions.
func AdminFromContext(ctx context.Context) (uuid.UUID, string, []string, bool) {
	userID, ok := ctx.Value(adminUserIDKey).(uuid.UUID)
	if !ok || userID == uuid.Nil {
		return uuid.Nil, "", nil, false
	}

	email, _ := ctx.Value(adminEmailKey).(string)
	perms, _ := ctx.Value(adminPermissionsKey).([]string)
	if perms == nil {
		perms = []string{}
	}
	return userID, email, perms, true
}

// AdminHasPermission reports whether the context admin has perm.
func AdminHasPermission(ctx context.Context, perm string) bool {
	_, _, perms, ok := AdminFromContext(ctx)
	if !ok {
		return false
	}
	for _, p := range perms {
		if p == perm {
			return true
		}
	}
	return false
}
