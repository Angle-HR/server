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
	RegionUnknown Region = ""
)

type contextKey int

const (
	regionContextKey contextKey = iota
	regionSourceContextKey
)

// Valid reports whether r is one of the four known regions.
func Valid(r Region) bool {
	switch r {
	case RegionUK, RegionUS, RegionAfrica, RegionEU:
		return true
	default:
		return false
	}
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
