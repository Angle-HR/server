package worker

import (
	"context"
	"log/slog"

	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/mailer"
)

type EmailWorker struct {
	fluvio.WorkerDefaults[mailer.EmailArgs]
	Mailer *mailer.Mailer
	Logger *slog.Logger
}

func (w *EmailWorker) Work(ctx context.Context, job *fluvio.Job[mailer.EmailArgs]) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}

	logger.Info("email job received", 
		"job_id", job.ID,
		"queue", job.Queue,
		"kind", job.Kind,
		"attempt", job.Attempt,
		"max_attempts", job.MaxAttempts,
		"type", job.Args.Type,
		"recipient", job.Args.Recipient,
	)

	job.Info("email job started", map[string]any{
		"type":      job.Args.Type,
		"recipient": job.Args.Recipient,
		"full_name": job.Args.FullName,
	})

	err := w.Mailer.Send(ctx, job.Args)
	if err != nil {
		logger.Error("email job failed",
			"job_id", job.ID,
			"attempt", job.Attempt,
			"type", job.Args.Type,
			"recipient", job.Args.Recipient,
			"error", err,
		)
		job.Error("email job failed", map[string]any{
			"type":      job.Args.Type,
			"recipient": job.Args.Recipient,
			"error":     err.Error(),
		})
		return err
	}

	logger.Info("email job completed",
		"job_id", job.ID,
		"attempt", job.Attempt,
		"type", job.Args.Type,
		"recipient", job.Args.Recipient,
	)
	job.Info("email job completed", map[string]any{
		"type":      job.Args.Type,
		"recipient": job.Args.Recipient,
	})

	return nil
}
