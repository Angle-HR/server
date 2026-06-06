package worker

import (
	"context"

	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/mailer"
)

type EmailWorker struct {
	fluvio.WorkerDefaults[mailer.EmailArgs]
	Mailer *mailer.Mailer
}

func (w *EmailWorker) Work(ctx context.Context, job *fluvio.Job[mailer.EmailArgs]) error {
	return w.Mailer.Send(ctx, job.Args)
}
