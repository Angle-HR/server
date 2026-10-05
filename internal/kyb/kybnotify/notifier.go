// Package kybnotify sends the company verification (KYB) lifecycle emails by
// putting them on the email queue.
package kybnotify

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/pkg/besteffort"
)

// RegionalDB reads the owner's contact details from the organization's region.
type RegionalDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// GlobalDB opens the transaction the email job is written in.
type GlobalDB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Enqueuer writes a job in a transaction.
type Enqueuer interface {
	EnqueueTx(ctx context.Context, tx fluvio.Tx, args fluvio.JobArgs, opts ...fluvio.EnqueueOption) (*fluvio.JobRow, error)
}

// Notifier implements kyb.Notifier for one region.
type Notifier struct {
	Regional RegionalDB
	Global   GlobalDB
	Enqueuer Enqueuer
}

// ErrNoRecipient means the organization or its owner could not be found.
var ErrNoRecipient = errors.New("kybnotify: no recipient for organization")

const ownerSQL = `
SELECT u.email, COALESCE(NULLIF(u.first_name, ''), NULLIF(u.legal_full_name, ''), ''), o.legal_name
FROM accounts.organizations o
JOIN accounts.users u ON u.id = o.owner_user_id
WHERE o.id = $1`

var emailTypes = map[kyb.Notice]string{
	kyb.NoticeFailed:      mailer.TypeKYBFailed,
	kyb.NoticeReviewQueue: mailer.TypeKYBReviewQueued,
	kyb.NoticeNudge1:      mailer.TypeKYBNudge1,
	kyb.NoticeNudge2:      mailer.TypeKYBNudge2,
}

// Notify queues the email for notice. It returns an error when the email could
// not be queued; the kyb package only logs it.
func (n *Notifier) Notify(
	ctx context.Context, organizationID string, notice kyb.Notice, reason kyb.FailureReason,
) (err error) {
	emailType, ok := emailTypes[notice]
	if !ok {
		return fmt.Errorf("kybnotify: unknown notice %q", notice)
	}
	var email, name, orgName string
	err = n.Regional.QueryRow(ctx, ownerSQL, organizationID).Scan(&email, &name, &orgName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRecipient
	}
	if err != nil {
		return fmt.Errorf("kybnotify: load owner: %w", err)
	}

	tx, err := n.Global.Begin(ctx)
	if err != nil {
		return fmt.Errorf("kybnotify: begin: %w", err)
	}
	defer func() {
		if err != nil {
			besteffort.Log(ctx, "kybnotify.rollback", tx.Rollback(ctx))
		}
	}()
	if _, err = n.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type: emailType, Recipient: email, FullName: name, OrganizationName: orgName, FailureReason: string(reason),
	}, queue.EmailEnqueueOptions()...); err != nil {
		return fmt.Errorf("kybnotify: enqueue: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("kybnotify: commit: %w", err)
	}
	return nil
}
