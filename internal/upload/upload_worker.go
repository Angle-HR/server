package upload

import (
	"context"
	"log/slog"

	fluvio "github.com/software78/fluvio"
)

// UploadWorker processes upload jobs from the Fluvio queue.
type UploadWorker struct {
	fluvio.WorkerDefaults[Args]
	Logger *slog.Logger
}

// Work handles a single upload job. This is a sample implementation that logs
// job metadata and succeeds without touching object storage.
func (w *UploadWorker) Work(ctx context.Context, job *fluvio.Job[Args]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("upload job received",
		"job_id", job.ID,
		"queue", job.Queue,
		"kind", job.Kind,
		"attempt", job.Attempt,
		"max_attempts", job.MaxAttempts,
		"bucket", job.Args.Bucket,
		"object_key", job.Args.ObjectKey,
	)

	job.Info("upload job started", map[string]any{
		"bucket":     job.Args.Bucket,
		"object_key": job.Args.ObjectKey,
	})

	logger.Info("upload job completed",
		"job_id", job.ID,
		"attempt", job.Attempt,
		"bucket", job.Args.Bucket,
		"object_key", job.Args.ObjectKey,
	)
	job.Info("upload job completed", map[string]any{
		"bucket":     job.Args.Bucket,
		"object_key": job.Args.ObjectKey,
	})

	return nil
}
