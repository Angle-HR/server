package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/appevents"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/identityevents"
	"github.com/Angle-HR/server/internal/identityhttp"
	"github.com/Angle-HR/server/internal/notifications"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/orghttp"
	"github.com/Angle-HR/server/pkg/config"
	"github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"

	"github.com/jackc/pgx/v5/pgxpool"
	fluvio "github.com/software78/fluvio"
)

// emailEnqueuer is what the handlers use to request an email inside their transaction.
type emailEnqueuer interface {
	EnqueueTx(ctx context.Context, tx fluvio.Tx, args fluvio.JobArgs, opts ...fluvio.EnqueueOption) (*fluvio.JobRow, error)
}

// busNeeded says whether this process must publish outbox rows: the events email transport, hiring job events or
// application events.
func busNeeded(cfg config.Config) bool {
	return cfg.NotificationsTransport == "events" || jobevents.Enabled() || appevents.Enabled() || (orgevents.Enabled() && !orghttp.Remote()) ||
		(identityevents.Enabled() && !identityhttp.Remote())
}

// selectEmailEnqueuer returns the Fluvio client (default) or the events outbox enqueuer. Whenever a feature needs
// the bus (events email transport, or HIRING_EVENTS=on) it also opens one bus connection and starts one outbox
// relay per database (global plus every region) and returns a stop func.
func selectEmailEnqueuer(
	ctx context.Context, cfg config.Config, log *slog.Logger,
	fluvioClient *fluvio.Client, global *pgxpool.Pool, router *dbrouter.DBRouter,
) (emailEnqueuer, func(), error) {
	if !busNeeded(cfg) {
		return fluvioClient, func() {}, nil
	}
	if cfg.NATSURL == "" {
		return nil, nil, fmt.Errorf("NOTIFICATIONS_TRANSPORT=events, HIRING_EVENTS=on, APPLICATIONS_EVENTS=on, ORG_EVENTS=on or IDENTITY_EVENTS=on requires NATS_URL")
	}
	stop, err := startOutboxRelays(ctx, cfg.NATSURL, log, global, router)
	if err != nil {
		return nil, nil, err
	}
	if cfg.NotificationsTransport != "events" {
		log.Info("outbox relays running (hiring job events)")
		return fluvioClient, stop, nil
	}
	log.Info("email transport: events (outbox relays running)")
	return notifications.NewEventEnqueuer(), stop, nil
}

// startOutboxRelays connects to the bus once and runs one relay for the global database and one for each region.
func startOutboxRelays(
	ctx context.Context, natsURL string, log *slog.Logger, global *pgxpool.Pool, router *dbrouter.DBRouter,
) (func(), error) {
	bus, err := nats.Connect(notifications.BusConfig(natsURL, "monolith"))
	if err != nil {
		return nil, err
	}
	relayCtx, cancel := context.WithCancel(ctx)
	start := func(name string, db outbox.DB) error {
		r, rerr := outbox.NewRelay(outbox.Config{DB: db, Publisher: bus, Logger: log.With("outbox", name)})
		if rerr != nil {
			return rerr
		}
		go func() { _ = r.Run(relayCtx) }()
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
