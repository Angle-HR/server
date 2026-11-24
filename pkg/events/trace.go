package events

import (
	"context"
	"regexp"
	"strings"
	"sync"
)

var traceParentRe = regexp.MustCompile(`^[0-9a-f]{2}-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`)

func validTraceParent(s string) bool {
	return traceParentRe.MatchString(s) && s[3:35] != strings.Repeat("0", 32) && s[36:52] != strings.Repeat("0", 16)
}

// Propagator moves trace context between a context.Context and the envelope. The default keeps the W3C values
// in the context itself; an application that uses OpenTelemetry installs one backed by the global propagator
// with SetPropagator (see docs/microservices/events.md). This package imports no tracing library.
type Propagator interface {
	// Inject returns the traceparent and tracestate of the span in ctx ("" when there is none).
	Inject(ctx context.Context) (traceParent, traceState string)
	// Extract returns a context that carries the given trace context.
	Extract(ctx context.Context, traceParent, traceState string) context.Context
}

type traceKey struct{}

type traceVal struct{ parent, state string }

// ContextPropagator is the default Propagator.
type ContextPropagator struct{}

// Inject implements Propagator.
func (ContextPropagator) Inject(ctx context.Context) (string, string) {
	v, _ := ctx.Value(traceKey{}).(traceVal)
	return v.parent, v.state
}

// Extract implements Propagator.
func (ContextPropagator) Extract(ctx context.Context, parent, state string) context.Context {
	return context.WithValue(ctx, traceKey{}, traceVal{parent, state})
}

var (
	propMu sync.RWMutex
	prop   Propagator = ContextPropagator{}
)

// SetPropagator installs the process wide Propagator. Call it once at start-up.
func SetPropagator(p Propagator) {
	if p == nil {
		p = ContextPropagator{}
	}
	propMu.Lock()
	prop = p
	propMu.Unlock()
}

func propagator() Propagator {
	propMu.RLock()
	defer propMu.RUnlock()
	return prop
}

// InjectTrace copies the trace context of ctx into the event unless it already carries one. Publishers and
// the outbox call it, so callers normally do not.
func InjectTrace(ctx context.Context, e Event) Event {
	if e.TraceParent != "" {
		return e
	}
	tp, ts := propagator().Inject(ctx)
	if validTraceParent(tp) {
		e.TraceParent, e.TraceState = tp, ts
	}
	return e
}

// ExtractTrace returns a context carrying the event's trace context. Subscribers call it before the handler.
func ExtractTrace(ctx context.Context, e Event) context.Context {
	if e.TraceParent == "" {
		return ctx
	}
	return propagator().Extract(ctx, e.TraceParent, e.TraceState)
}

// WithTraceParent returns a context carrying a W3C traceparent for the default propagator.
func WithTraceParent(ctx context.Context, traceParent string) context.Context {
	return context.WithValue(ctx, traceKey{}, traceVal{parent: traceParent})
}
