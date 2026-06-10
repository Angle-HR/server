package queue

import (
	"context"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	fluvio "github.com/software78/fluvio"
	"github.com/software78/fluvio/postgres"
)

const DefaultMaxAttempts int16 = 25

// DefaultEnqueueOptions returns enqueue options applied to all jobs.
func DefaultEnqueueOptions() []fluvio.EnqueueOption {
	return []fluvio.EnqueueOption{
		fluvio.WithMaxAttempts(DefaultMaxAttempts),
	}
}

func hostname() string {
	if id := os.Getenv("HOSTNAME"); id != "" {
		return id
	}
	return "local"
}

func postgresConfig(leaderPrefix string) postgres.Config {
	return postgres.Config{
		UseLeaseTable: true,
		LeaderID:      leaderPrefix + "-" + hostname(),
		PollOnly:      os.Getenv("FLUVIO_POLL_ONLY") == "true",
	}
}

func driver(pool *pgxpool.Pool, leaderPrefix string) *postgres.Driver {
	return postgres.New(pool, postgresConfig(leaderPrefix))
}

// Migrate applies Fluvio schema migrations on the global database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return driver(pool, "migrate").Migrate(ctx)
}

// NewInsertClient returns an insert-only Fluvio client (no queue processing).
func NewInsertClient(pool *pgxpool.Pool) (*fluvio.Client, error) {
	return fluvio.NewClient(driver(pool, "server"), &fluvio.Config{
		Workers: fluvio.NewWorkers(),
	})
}

// NewWorkerClient returns a Fluvio client configured to process jobs.
func NewWorkerClient(pool *pgxpool.Pool, workerName string, workers *fluvio.Workers, logger *slog.Logger) (*fluvio.Client, error) {
	return fluvio.NewClient(driver(pool, workerName), &fluvio.Config{
		Queues: map[string]fluvio.QueueConfig{
			fluvio.QueueDefault: {MaxWorkers: 10},
		},
		Workers:  workers,
		Logger:   logger,
		PollOnly: os.Getenv("FLUVIO_POLL_ONLY") == "true",
	})
}
