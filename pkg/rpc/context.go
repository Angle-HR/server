// Package rpc carries the call context (region, company, user, permissions, trace
// and request ids) between services as gRPC metadata. It holds no cloud-specific
// code and depends only on gRPC and the generated common messages.
package rpc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
)

// Metadata keys. Keys are lowercase as gRPC requires. The trace id travels in the W3C
// "traceparent" header so OpenTelemetry propagators and this package interoperate.
const (
	MDRegion      = "x-openhr-region"
	MDCompanyID   = "x-openhr-company-id"
	MDUserID      = "x-openhr-user-id"
	MDPermissions = "x-openhr-permissions"
	MDRequestID   = "x-request-id"
	MDActorType   = "x-openhr-actor-type"
	MDTraceparent = "traceparent"
)

type ctxKey struct{}

// WithCallContext returns ctx carrying cc. A client interceptor forwards it.
func WithCallContext(ctx context.Context, cc *commonv1.CallContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, cc)
}

// CallContextFrom returns the call context of ctx. The second result is false when
// none is set; the returned value is then a non-nil empty context, safe to read.
func CallContextFrom(ctx context.Context) (*commonv1.CallContext, bool) {
	if cc, ok := ctx.Value(ctxKey{}).(*commonv1.CallContext); ok && cc != nil {
		return cc, true
	}
	return &commonv1.CallContext{}, false
}

// HasPermission reports whether the caller holds permission p.
func HasPermission(cc *commonv1.CallContext, p string) bool {
	for _, have := range cc.GetPermissions() {
		if have == p {
			return true
		}
	}
	return false
}

// NewTraceID returns a random W3C trace id (32 hex chars, never all zero).
func NewTraceID() string { return randomHex(16) }

// NewSpanID returns a random W3C span id (16 hex chars, never all zero).
func NewSpanID() string { return randomHex(8) }

// NewRequestID returns a random request id.
func NewRequestID() string { return randomHex(12) }

func randomHex(n int) string {
	b := make([]byte, n)
	for {
		if _, err := rand.Read(b); err != nil {
			panic("rpc: no entropy: " + err.Error())
		}
		for _, x := range b {
			if x != 0 {
				return hex.EncodeToString(b)
			}
		}
	}
}

// FormatTraceparent builds a version-00 traceparent header, sampled.
func FormatTraceparent(traceID, spanID string) string {
	return "00-" + traceID + "-" + spanID + "-01"
}

// ParseTraceparent extracts the trace id from a traceparent header. ok is false for
// malformed or all-zero values.
func ParseTraceparent(h string) (traceID string, ok bool) {
	parts := strings.Split(strings.TrimSpace(h), "-")
	if len(parts) < 4 || len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return "", false
	}
	if !isHex(parts[1]) || parts[1] == strings.Repeat("0", 32) {
		return "", false
	}
	return parts[1], true
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
