package identitysvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// StartOutboxRelays connects to NATS once and runs one outbox relay for the global database and one per regional
// database. Mail requests (notifications.email.requested.v1) and identity events are written to the outbox of the
// database holding the change, in the same transaction, so the relays have to cover all of them. The returned func
// stops the relays and closes the connection.
func StartOutboxRelays(
	ctx context.Context, natsURL string, log *slog.Logger, global *pgxpool.Pool, router *dbrouter.DBRouter,
) (func(), error) {
	if natsURL == "" {
		return nil, errors.New("the identity service publishes mail requests and events through the outbox and requires NATS_URL")
	}
	bus, err := natsbus.Connect(notifications.BusConfig(natsURL, "identity"))
	if err != nil {
		return nil, fmt.Errorf("connect nats: %w", err)
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
