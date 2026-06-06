package queue

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	fluvio "github.com/software78/fluvio"
	"github.com/software78/fluvio/postgres"
)

func driver(pool *pgxpool.Pool) *postgres.Driver {
	return postgres.New(pool, postgres.Config{})
}

// Migrate applies Fluvio schema migrations on the global database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return driver(pool).Migrate(ctx)
}

// NewInsertClient returns an insert-only Fluvio client (no queue processing).
func NewInsertClient(pool *pgxpool.Pool) (*fluvio.Client, error) {
	return fluvio.NewClient(driver(pool), &fluvio.Config{
		Workers: fluvio.NewWorkers(),
	})
}

// NewWorkerClient returns a Fluvio client configured to process jobs.
func NewWorkerClient(pool *pgxpool.Pool, workers *fluvio.Workers, logger *slog.Logger) (*fluvio.Client, error) {
	return fluvio.NewClient(driver(pool), &fluvio.Config{
		Queues: map[string]fluvio.QueueConfig{
			fluvio.QueueDefault: {MaxWorkers: 10},
		},
		Workers: workers,
		Logger:  logger,
	})
}
