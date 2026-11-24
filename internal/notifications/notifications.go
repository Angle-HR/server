// Package notifications is the event contract and the glue between the monolith (producer) and the notifications
// service (consumer) for transactional email.
//
// Producers keep their existing call, EnqueueTx(ctx, tx, mailer.EmailArgs), but the EventEnqueuer writes a
// notifications.email.requested.v1 event to the events outbox in the caller's transaction instead of a Fluvio job.
// The notifications service subscribes, de-duplicates through its inbox, and sends through the mailer.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"

	"github.com/jackc/pgx/v5"
)

const (
	// TypeEmailRequested asks the notifications service to send one email. The payload is mailer.EmailArgs.
	TypeEmailRequested = "notifications.email.requested.v1"
	// Subject filter the notifications service subscribes to.
	SubjectFilter = "notifications.email.>"
	// Consumer is the durable consumer name; replicas of the service share it.
	Consumer = "notifications-email"
	// DefaultSource identifies the monolith as producer until it is split further.
	DefaultSource = "/services/monolith"
)

// Sender delivers one email. *mailer.Mailer implements it.
type Sender interface {
	Send(ctx context.Context, args mailer.EmailArgs) error
}

// EventEnqueuer is a drop-in replacement for the Fluvio client's EnqueueTx for email jobs.
type EventEnqueuer struct{ Source string }

// NewEventEnqueuer returns an enqueuer that writes to the events outbox.
func NewEventEnqueuer() *EventEnqueuer { return &EventEnqueuer{Source: DefaultSource} }

// EnqueueTx stores the email request in the outbox using tx, which must be a pgx transaction (or anything with
// Exec). The event is published only if tx commits. The returned job row is always nil.
func (e *EventEnqueuer) EnqueueTx(
	ctx context.Context, tx fluvio.Tx, args fluvio.JobArgs, _ ...fluvio.EnqueueOption,
) (*fluvio.JobRow, error) {
	ea, ok := args.(mailer.EmailArgs)
	if !ok {
		return nil, fmt.Errorf("notifications: unsupported job args %T", args)
	}
	ex, ok := tx.(outbox.Execer)
	if !ok {
		return nil, fmt.Errorf("notifications: transaction %T cannot write to the outbox", tx)
	}
	src := e.Source
	if src == "" {
		src = DefaultSource
	}
	ev, err := events.NewJSON(src, TypeEmailRequested, ea)
	if err != nil {
		return nil, err
	}
	ev = ev.WithSubject(ea.Type)
	return nil, outbox.Enqueue(ctx, ex, ev)
}

// Handler returns the event handler for the notifications service: decode, send, record in the inbox.
func Handler(db inbox.DB, sender Sender) events.Handler {
	return inbox.Handle(db, Consumer, func(ctx context.Context, _ pgx.Tx, e events.Event) error {
		var args mailer.EmailArgs
		if err := e.DecodeJSON(&args); err != nil {
			return events.Terminal(fmt.Errorf("decode email request: %w", err))
		}
		if args.Recipient == "" {
			return events.Terminal(errors.New("email request has no recipient"))
		}
		return sender.Send(ctx, args)
	})
}

// StreamMaxAge is how long email requests are kept on the bus. They carry the recipient address and one-time codes
// (valid for minutes), so they are kept far shorter than the 7 day default. Every producer and the service must use
// BusConfig so the stream is created with the same limit.
// NOTIFICATIONS_STREAM_MAX_AGE overrides it (see StreamMaxAgeFromEnv).
const StreamMaxAge = 24 * time.Hour

// Bounds for NOTIFICATIONS_STREAM_MAX_AGE: long enough for the retry schedule (about 3 minutes) and an outage of the
// service, short enough that one-time codes do not linger.
const (
	minStreamMaxAge = 5 * time.Minute
	maxStreamMaxAge = 7 * 24 * time.Hour
)

// StreamMaxAgeFromEnv returns NOTIFICATIONS_STREAM_MAX_AGE (a Go duration such as "2h") or StreamMaxAge when it is
// unset. A value that does not parse or is outside 5m..168h is ignored with a warning, so a typo cannot make the
// stream drop pending mail or keep it for weeks.
func StreamMaxAgeFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("NOTIFICATIONS_STREAM_MAX_AGE"))
	if raw == "" {
		return StreamMaxAge
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < minStreamMaxAge || d > maxStreamMaxAge {
		slog.Warn("ignoring NOTIFICATIONS_STREAM_MAX_AGE (want a duration between 5m and 168h)",
			"value", raw, "using", StreamMaxAge.String())
		return StreamMaxAge
	}
	return d
}

// BusConfig returns the NATS configuration for a process that publishes or consumes notification events.
func BusConfig(url, name string) natsbus.Config {
	return natsbus.Config{
		URL: url, Name: name,
		DomainMaxAge: map[string]time.Duration{"notifications": StreamMaxAgeFromEnv()},
	}
}

// BusSender publishes an email request straight to the bus, for jobs that already keep their own durable queue
// (the hiring outbox marks a row sent only after Send returns, so a failed publish is retried by the job).
type BusSender struct {
	Pub    events.Publisher
	Source string
}

// Send implements the hiring outbox Sender.
func (b BusSender) Send(ctx context.Context, args mailer.EmailArgs) error {
	src := b.Source
	if src == "" {
		src = DefaultSource
	}
	ev, err := events.NewJSON(src, TypeEmailRequested, args)
	if err != nil {
		return err
	}
	return b.Pub.Publish(ctx, ev.WithSubject(args.Type))
}
