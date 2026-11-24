package events

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Publisher sends events to the bus. Publish returns once the broker has accepted the event (at-least-once).
// Publishing an event whose ID the broker has already seen is a silent no-op within its de-duplication window.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Handler processes one event. Return nil to acknowledge it. Return any other error to have it redelivered
// after a delay (see Retry to choose the delay, Terminal to skip the retries). After MaxDeliveries attempts the
// event is published to the dead-letter subject "dlq.<consumer>.<type>" and acknowledged.
//
// The handler may run more than once for the same event (at-least-once): make it idempotent, for example with
// pkg/events/inbox.
type Handler func(ctx context.Context, e Event) error

// Subscriber attaches a handler to a durable, named consumer.
//
// consumer names the durable position: all Subscribe calls with the same name share the work (competing
// consumers); different names each receive every matching event. subjects are NATS style filters: "*" matches
// one token and ">" matches the rest. The first token (domain) must be literal.
type Subscriber interface {
	Subscribe(ctx context.Context, consumer string, subjects []string, h Handler,
		opts ...SubscribeOption) (Subscription, error)
}

// Subscription is a running consumer.
type Subscription interface {
	// Stop stops taking new events, lets the handlers that are running finish, and hands back the rest to the
	// broker (they are redelivered). If ctx ends first, running handlers see their context cancelled.
	Stop(ctx context.Context) error
}

// Bus is both ports plus Close.
type Bus interface {
	Publisher
	Subscriber
	// Close releases the connection. Subscriptions should be stopped first.
	Close() error
}

// Delivery describes the current delivery attempt; read it with DeliveryFrom.
type Delivery struct {
	Consumer string
	Attempt  int // 1 for the first delivery
	Max      int
}

type deliveryKey struct{}

// ContextWithDelivery is for adapters.
func ContextWithDelivery(ctx context.Context, d Delivery) context.Context {
	return context.WithValue(ctx, deliveryKey{}, d)
}

// DeliveryFrom returns the delivery info inside a handler.
func DeliveryFrom(ctx context.Context) (Delivery, bool) {
	d, ok := ctx.Value(deliveryKey{}).(Delivery)
	return d, ok
}

// SubscribeOptions tune a consumer. Zero values use the defaults.
type SubscribeOptions struct {
	MaxDeliveries int             // attempts before dead-lettering; default 5
	AckWait       time.Duration   // how long the broker waits for an ack before redelivering; default 30s
	Backoff       []time.Duration // delay after the 1st, 2nd... failure; the last repeats; default 1s,5s,30s,2m
	Concurrency   int             // handlers running at once; default 8
}

// Defaults for SubscribeOptions.
const (
	DefaultMaxDeliveries = 5
	DefaultAckWait       = 30 * time.Second
	DefaultConcurrency   = 8
)

// DefaultBackoff is the retry delay schedule.
var DefaultBackoff = []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute}

// SubscribeOption changes SubscribeOptions.
type SubscribeOption func(*SubscribeOptions)

// WithMaxDeliveries sets the attempts before dead-lettering.
func WithMaxDeliveries(n int) SubscribeOption {
	return func(o *SubscribeOptions) { o.MaxDeliveries = n }
}

// WithAckWait sets the broker ack timeout.
func WithAckWait(d time.Duration) SubscribeOption { return func(o *SubscribeOptions) { o.AckWait = d } }

// WithBackoff sets the retry delays.
func WithBackoff(d ...time.Duration) SubscribeOption {
	return func(o *SubscribeOptions) { o.Backoff = d }
}

// WithConcurrency sets how many handlers run at once.
func WithConcurrency(n int) SubscribeOption { return func(o *SubscribeOptions) { o.Concurrency = n } }

// Resolve applies options and defaults.
func Resolve(opts ...SubscribeOption) SubscribeOptions {
	var o SubscribeOptions
	for _, f := range opts {
		f(&o)
	}
	if o.MaxDeliveries <= 0 {
		o.MaxDeliveries = DefaultMaxDeliveries
	}
	if o.AckWait <= 0 {
		o.AckWait = DefaultAckWait
	}
	if len(o.Backoff) == 0 {
		o.Backoff = DefaultBackoff
	}
	if o.Concurrency <= 0 {
		o.Concurrency = DefaultConcurrency
	}
	return o
}

// DelayAfter returns the retry delay after the given failed attempt (1-based).
func (o SubscribeOptions) DelayAfter(attempt int) time.Duration {
	i := attempt - 1
	if i < 0 {
		i = 0
	}
	if i >= len(o.Backoff) {
		i = len(o.Backoff) - 1
	}
	return o.Backoff[i]
}

type retryError struct {
	err   error
	after time.Duration
}

func (r *retryError) Error() string { return r.err.Error() }
func (r *retryError) Unwrap() error { return r.err }

// Retry makes the failure retry after d instead of the backoff schedule.
func Retry(err error, d time.Duration) error {
	if err == nil {
		return nil
	}
	return &retryError{err: err, after: d}
}

// RetryAfter returns the delay chosen with Retry.
func RetryAfter(err error) (time.Duration, bool) {
	var r *retryError
	if errors.As(err, &r) {
		return r.after, true
	}
	return 0, false
}

type terminalError struct{ err error }

func (t *terminalError) Error() string { return t.err.Error() }
func (t *terminalError) Unwrap() error { return t.err }

// Terminal marks a failure that retrying cannot fix: the event goes straight to the dead-letter subject.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &terminalError{err}
}

// IsTerminal reports whether err was wrapped with Terminal.
func IsTerminal(err error) bool {
	var t *terminalError
	return errors.As(err, &t)
}

// DLQSubject is where an event that exhausted its deliveries goes.
func DLQSubject(consumer, typ string) string { return "dlq." + SanitizeConsumer(consumer) + "." + typ }

// SanitizeConsumer makes a consumer name safe as a subject token and a durable name.
func SanitizeConsumer(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, s)
}

// Match reports whether a NATS style filter matches a subject.
func Match(filter, subject string) bool {
	f, s := strings.Split(filter, "."), strings.Split(subject, ".")
	for i, t := range f {
		if t == ">" {
			return len(s) > i
		}
		if i >= len(s) || (t != "*" && t != s[i]) {
			return false
		}
	}
	return len(f) == len(s)
}
