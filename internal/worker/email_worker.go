package worker

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/Angle-HR/server/internal/mailer"
)

type EmailWorker struct {
	river.WorkerDefaults[mailer.EmailArgs]
	Mailer *mailer.Mailer
}

func (w *EmailWorker) Work(ctx context.Context, job *river.Job[mailer.EmailArgs]) error {
	return w.Mailer.Send(ctx, job.Args)
}
