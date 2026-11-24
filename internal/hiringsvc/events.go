package hiringsvc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// StartOutboxRelays connects to NATS once and runs one outbox relay for the global database and one per regional
// database, so the hiring.job.* events written in the same transaction as the change reach the bus (HIRING_EVENTS=on).
// Relays claim rows with SKIP LOCKED, so a second relay on the same outbox is safe, but one active relay per
// database keeps a job's events in order: with HIRING_REMOTE=on the monolith writes no hiring events, so run
// HIRING_EVENTS=on here and leave the monolith's relays to its other event types. The returned func stops the relays and closes the connection.
func StartOutboxRelays(
	ctx context.Context, natsURL string, log *slog.Logger, global outbox.DB, router *dbrouter.DBRouter,
) (func(), error) {
	if natsURL == "" {
		return nil, fmt.Errorf("HIRING_EVENTS=on requires NATS_URL")
	}
	bus, err := natsbus.Connect(notifications.BusConfig(natsURL, "hiring"))
	if err != nil {
		return nil, err
	}
	relayCtx, cancel := context.WithCancel(ctx)
	start := func(name string, db outbox.DB) error {
		rel, rerr := outbox.NewRelay(outbox.Config{DB: db, Publisher: bus, Logger: log.With("outbox", name)})
		if rerr != nil {
			return rerr
		}
		go func() { _ = rel.Run(relayCtx) }()
		return nil
	}
	if err = start("global", global); err != nil {
		cancel()
		_ = bus.Close()
		return nil, err
	}
	for _, reg := range dbrouter.Regions() {
		pool, perr := router.DB(reg)
		if perr != nil {
			continue
		}
		if err = start(string(reg), pool); err != nil {
			cancel()
			_ = bus.Close()
			return nil, err
		}
	}
	return func() { cancel(); _ = bus.Close() }, nil
}
