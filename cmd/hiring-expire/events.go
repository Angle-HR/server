package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/internal/notifications"
	natsbus "github.com/Angle-HR/server/pkg/events/nats"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// drainOutboxes publishes the job events the run enqueued (HIRING_EVENTS=on) before the process exits.
func drainOutboxes(ctx context.Context, log *slog.Logger, pools map[string]outbox.DB) error {
	url := os.Getenv("NATS_URL")
	if url == "" {
		return errors.New("HIRING_EVENTS=on requires NATS_URL")
	}
	bus, err := natsbus.Connect(notifications.BusConfig(url, "hiring-expire"))
	if err != nil {
		return err
	}
	defer func() { _ = bus.Close() }()
	return jobevents.Drain(ctx, log, bus, pools)
}
