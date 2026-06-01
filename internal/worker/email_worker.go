package worker

import (
	"context"

	"github.com/riverqueue/river"

	"github.com/Angle-HR/server/internal/mailer"
)

const (
	EmailTypeWaitlistConfirmation = "waitlist_confirmation"
	EmailTypeMoreInfoAck          = "more_info_ack"
)

type EmailArgs struct {
	Type      string `json:"type"`
	Recipient string `json:"recipient"`
	FullName  string `json:"full_name"`
}

func (EmailArgs) Kind() string {
	return "email"
}

type EmailWorker struct {
	river.WorkerDefaults[EmailArgs]
	Mailer *mailer.Mailer
}

func (w *EmailWorker) Work(ctx context.Context, job *river.Job[EmailArgs]) error {
	return w.Mailer.Send(ctx, mailer.EmailArgs{
		Type:      job.Args.Type,
		Recipient: job.Args.Recipient,
		FullName:  job.Args.FullName,
	})
}
