package applicationsvc

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// StartOutboxRelays connects to NATS once and runs one outbox relay per regional database, so the application
// events written in the same transaction as the change reach the bus. Application events live in the regional
// outbox of the application's region; the global database has none from this service. The returned func stops
// the relays and closes the connection.
func StartOutboxRelays(ctx context.Context, natsURL string, log *slog.Logger, router *dbrouter.DBRouter) (func(), error) {
	if natsURL == "" {
		return nil, fmt.Errorf("APPLICATIONS_EVENTS=on requires NATS_URL")
	}
	bus, err := natsbus.Connect(notifications.BusConfig(natsURL, "applications"))
	if err != nil {
		return nil, err
	}
	relayCtx, cancel := context.WithCancel(ctx)
	for _, reg := range dbrouter.Regions() {
		pool, perr := router.DB(reg)
		if perr != nil {
			continue
		}
		rel, rerr := outbox.NewRelay(outbox.Config{DB: pool, Publisher: bus, Logger: log.With("outbox", string(reg))})
		if rerr != nil {
			cancel()
			_ = bus.Close()
			return nil, rerr
		}
		go func() { _ = rel.Run(relayCtx) }()
	}
	return func() { cancel(); _ = bus.Close() }, nil
}
