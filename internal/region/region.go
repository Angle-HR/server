// Package region resolves geographic regions for incoming HTTP requests.
package region

import (
	"context"
	"fmt"
)

// Region is a geographic deployment region identifier.
type Region string

const (
	RegionUK      Region = "uk"
	RegionUS      Region = "us"
	RegionAfrica  Region = "africa"
	RegionEU      Region = "eu"
	RegionAsia    Region = "asia"
	RegionUnknown Region = ""

	// RegionGlobal is not a real deployment region — it has no entry in
	// dbrouter's regional pool map and never appears in All(). It marks an
	// account that has signed up (and may be fully authenticated) but hasn't
	// yet told us where it is: its data lives in the global database's
	// holding tables (accounts.pending_users / accounts.pending_onboarding_progress)
	// until an onboarding step resolves a real region and migrates it.
	// It's included in Valid() so JWTs and the auth context can carry it —
	// pending accounts still need to log in and call protected onboarding
	// endpoints — but callers that turn a region into a database pool must
	// special-case it (see internal/handler.resolvePool).
	RegionGlobal Region = "global"
)

type contextKey int

const (
	regionContextKey contextKey = iota
	regionSourceContextKey
)

// Valid reports whether r is a recognized region value — one of the five real
// deployment regions, or the RegionGlobal pending sentinel.
func Valid(r Region) bool {
	switch r {
	case RegionUK, RegionUS, RegionAfrica, RegionEU, RegionAsia, RegionGlobal:
		return true
	default:
		return false
	}
}

// All returns every configured deployment region. It deliberately excludes
// RegionGlobal, which has no dedicated database and isn't a deployment.
func All() []Region {
	return []Region{RegionUK, RegionUS, RegionAfrica, RegionEU, RegionAsia}
}

// WithRegion returns a context carrying the resolved region and source.
func WithRegion(ctx context.Context, region Region, source string) context.Context {
	ctx = context.WithValue(ctx, regionContextKey, region)
	return context.WithValue(ctx, regionSourceContextKey, source)
}

// GetRegion returns the region stored on ctx by region middleware.
func GetRegion(ctx context.Context) (Region, bool) {
	region, ok := ctx.Value(regionContextKey).(Region)
	if !ok || !Valid(region) {
		return RegionUnknown, false
	}

	return region, true
}

// MustGetRegion returns the region on ctx or panics if unset.
// Use only after region middleware has run.
func MustGetRegion(ctx context.Context) Region {
	region, ok := GetRegion(ctx)
	if !ok {
		panic("region: not set on context")
	}

	return region
}

// GetRegionSource returns the resolution source string (jwt, subdomain, db, param, geo).
func GetRegionSource(ctx context.Context) (string, bool) {
	source, ok := ctx.Value(regionSourceContextKey).(string)
	if !ok || source == "" {
		return "", false
	}

	return source, true
}

// ParseRegion parses s into a Region and validates it.
func ParseRegion(s string) (Region, error) {
	r := Region(s)
	if !Valid(r) {
		return RegionUnknown, fmt.Errorf("region: unknown value %q", s)
	}

	return r, nil
}
