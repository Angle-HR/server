package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/region"
)

type contextKey string

const (
	userIDKey contextKey = "authUserID"
	regionKey contextKey = "authRegion"
)

// WithUser stores authenticated user ID on the context.
func WithUser(ctx context.Context, userID uuid.UUID, reg region.Region) context.Context {
	ctx = context.WithValue(ctx, userIDKey, userID)
	return context.WithValue(ctx, regionKey, reg)
}

// UserFromContext returns the authenticated user ID and region.
func UserFromContext(ctx context.Context) (uuid.UUID, region.Region, bool) {
	userID, ok := ctx.Value(userIDKey).(uuid.UUID)
	if !ok || userID == uuid.Nil {
		return uuid.Nil, region.RegionUnknown, false
	}

	reg, ok := ctx.Value(regionKey).(region.Region)
	if !ok || !region.Valid(reg) {
		return uuid.Nil, region.RegionUnknown, false
	}

	return userID, reg, true
}
