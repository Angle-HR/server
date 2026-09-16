package queue

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	fluvio "github.com/software78/fluvio"
	"github.com/software78/fluvio/postgres"
)

const DefaultMaxAttempts int16 = 10

const (
	QueueEmail = "email"
)

// EmailEnqueueOptions returns enqueue options for email jobs.
func EmailEnqueueOptions() []fluvio.EnqueueOption {
	return []fluvio.EnqueueOption{
		fluvio.WithQueue(QueueEmail),
		fluvio.WithMaxAttempts(DefaultMaxAttempts),
	}
}

// EmailWorkerQueues returns the queue config for the email worker.
func EmailWorkerQueues() map[string]fluvio.QueueConfig {
	return map[string]fluvio.QueueConfig{
		QueueEmail: {MaxWorkers: 10},
	}
}

func postgresConfig() postgres.Config {
	return postgres.Config{
		PollOnly: os.Getenv("FLUVIO_POLL_ONLY") == "true",
	}
}

func driver(pool *pgxpool.Pool) *postgres.Driver {
	return postgres.New(pool, postgresConfig())
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

// NewWorkerClient returns a Fluvio client configured to process jobs from the given queues.
func NewWorkerClient(pool *pgxpool.Pool, queues map[string]fluvio.QueueConfig, workers *fluvio.Workers, logger *slog.Logger) (*fluvio.Client, error) {
	return fluvio.NewClient(driver(pool), &fluvio.Config{
		Queues:   queues,
		Workers:  workers,
		Logger:   logger,
		PollOnly: os.Getenv("FLUVIO_POLL_ONLY") == "true",
	})
}
