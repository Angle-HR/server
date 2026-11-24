package jobevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// Drain publishes everything due in the given outboxes (name -> database) and returns when none is left, for
// one-shot jobs that have no long-running relay. Rows that fail stay in the outbox with their retry schedule.
func Drain(ctx context.Context, log *slog.Logger, pub events.Publisher, dbs map[string]outbox.DB) error {
	var errs []error
	for name, db := range dbs {
		r, err := outbox.NewRelay(outbox.Config{DB: db, Publisher: pub, Logger: log.With("outbox", name)})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for range 200 { // RunOnce handles one batch; stop when nothing is due
			res, runErr := r.RunOnce(ctx)
			if runErr != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, runErr))
				break
			}
			if res.Published+res.Retry+res.Dead == 0 {
				break
			}
			if res.Retry+res.Dead > 0 {
				errs = append(errs, fmt.Errorf("%s: %d retry, %d dead", name, res.Retry, res.Dead))
				break
			}
		}
	}
	return errors.Join(errs...)
}
