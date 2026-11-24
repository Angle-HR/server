package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb/kybnotify"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"

	"github.com/jackc/pgx/v5/pgxpool"
)

// eventsTransport returns an outbox enqueuer, a func that publishes everything the run left in the outboxes of the
// global and regional databases, and a cleanup func.
func eventsTransport(
	ctx context.Context, log *slog.Logger, global *pgxpool.Pool, router *dbrouter.DBRouter,
) (kybnotify.Enqueuer, func(context.Context) error, func(), error) {
	url := os.Getenv("NATS_URL")
	if url == "" {
		return nil, nil, nil, errors.New("NOTIFICATIONS_TRANSPORT=events requires NATS_URL")
	}
	bus, err := natsbus.Connect(notifications.BusConfig(url, "kyb-sweep"))
	if err != nil {
		return nil, nil, nil, err
	}
	pools := map[string]outbox.DB{"global": global}
	for _, reg := range dbrouter.Regions() {
		if p, perr := router.DB(reg); perr == nil {
			pools[string(reg)] = p
		}
	}
	drain := func(ctx context.Context) error {
		var errs []error
		for name, db := range pools {
			r, rerr := outbox.NewRelay(outbox.Config{DB: db, Publisher: bus, Logger: log.With("outbox", name)})
			if rerr != nil {
				errs = append(errs, rerr)
				continue
			}
			for range 50 { // RunOnce handles one batch; stop when nothing is due
				res, runErr := r.RunOnce(ctx)
				if runErr != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, runErr))
					break
				}
				if res.Published+res.Retry+res.Dead == 0 {
					break
				}
			}
		}
		return errors.Join(errs...)
	}
	return notifications.NewEventEnqueuer(), drain, func() { _ = bus.Close() }, nil
}
