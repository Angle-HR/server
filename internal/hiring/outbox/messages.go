package outbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/mailer"
)

// DueMessage is a message to a candidate that is ready to go out. The recipient comes from the application the
// message belongs to, never from the message.
type DueMessage struct {
	ID        string
	Recipient string
	Name      string
	Company   string
	JobTitle  string
	Subject   string
	Body      string
	ReplyTo   string
	ICS       string
	// Secret is the token of an offer link; the body's {{offer_link}} becomes the link.
	Secret      string
	Attachments []DueAttachment
	Attempts    int // including this one
}

// DueAttachment is a file to send with a message, kept in the regional bucket.
type DueAttachment struct{ Key, Filename, ContentType string }

// Files reads objects of the regional bucket. blob.BlobStore implements it.
type Files interface {
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// MessageStore is one region's database as the message sender uses it.
type MessageStore interface {
	// LeaseMessages returns up to limit scheduled or queued messages that are due and hides them from other runs
	// for the lease time.
	LeaseMessages(ctx context.Context, tenantID string, now time.Time, limit int, lease time.Duration) ([]DueMessage, error)
	MarkMessageSent(ctx context.Context, tenantID, id string, now time.Time) error
	// MarkMessageFailed records a failed attempt; with final true the message is given up on, otherwise it is
	// retried at next.
	MarkMessageFailed(ctx context.Context, tenantID, id, reason string, final bool, next time.Time) error
}

// OfferLink is the page where a candidate reads and answers an offer.
func OfferLink(base, token string) string { return strings.TrimRight(base, "/") + "/offers/" + token }

// MessageArgs turns a due message into the arguments of the mailer. linkBase is the candidate-facing site, for the
// link of an offer.
func MessageArgs(m DueMessage, linkBase string) (mailer.EmailArgs, error) {
	if strings.TrimSpace(m.Recipient) == "" {
		return mailer.EmailArgs{}, errors.New("outbox: message has no recipient")
	}
	if m.Secret != "" {
		if linkBase == "" {
			return mailer.EmailArgs{}, errors.New("outbox: the offer link needs the site address (APP_URL)")
		}
		m.Body = strings.ReplaceAll(m.Body, "{{offer_link}}", OfferLink(linkBase, m.Secret))
	}
	return mailer.EmailArgs{
		Type: mailer.TypeCandidateMessage, Recipient: m.Recipient, FullName: m.Name, OrganizationName: m.Company,
		JobTitle: m.JobTitle, Subject: m.Subject, MessageBody: m.Body, ReplyTo: m.ReplyTo, ICS: m.ICS,
	}, nil
}

// RunMessages sends every due message to a candidate once, with the same leasing and retry rules as emails.
func (w *Worker) RunMessages(ctx context.Context, store MessageStore) (Result, error) {
	var res Result
	now := func() time.Time {
		if w.Now != nil {
			return w.Now()
		}
		return time.Now()
	}
	max := w.MaxAttempts
	if max <= 0 {
		max = MaxAttempts
	}
	tenants, err := w.Tenants(ctx)
	if err != nil {
		return res, fmt.Errorf("outbox: list companies: %w", err)
	}
	var firstErr error
	for _, tid := range tenants {
		done := 0
		for done < maxPerCompany {
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			list, err := store.LeaseMessages(ctx, tid, now(), batchSize, leaseTime)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("outbox: lease messages: %w", err)
				}
				break
			}
			for _, m := range list {
				done++
				args, err := MessageArgs(m, w.LinkBase)
				if err == nil {
					err = w.attach(ctx, m, &args)
				}
				if err == nil {
					err = w.Sender.Send(ctx, args)
				}
				if err == nil {
					if merr := store.MarkMessageSent(ctx, tid, m.ID, now()); merr != nil && w.Logger != nil {
						w.Logger.Error("outbox: mark message sent", "error", merr)
					}
					res.Sent++
					continue
				}
				final := m.Attempts >= max
				if merr := store.MarkMessageFailed(ctx, tid, m.ID, truncate(err.Error(), 300), final, now().Add(delay(m.Attempts))); merr != nil && w.Logger != nil {
					w.Logger.Error("outbox: mark message failed", "error", merr)
				}
				if final {
					res.Failed++
				} else {
					res.Retry++
				}
				if w.Logger != nil {
					w.Logger.Warn("outbox: message not sent", "attempt", m.Attempts, "final", final)
				}
			}
			if len(list) < batchSize {
				break
			}
		}
	}
	return res, firstErr
}

// maxMailBytes is the most attachment data one email carries: it stays under what mail servers accept.
const maxMailBytes = 10 << 20

// attach reads the files of a message from the bucket into the mailer arguments.
func (w *Worker) attach(ctx context.Context, m DueMessage, args *mailer.EmailArgs) error {
	if len(m.Attachments) == 0 {
		return nil
	}
	if w.Files == nil {
		return errors.New("outbox: no bucket to read attachments from")
	}
	total := 0
	for _, a := range m.Attachments {
		r, err := w.Files.Open(ctx, a.Key)
		if err != nil {
			return fmt.Errorf("outbox: open attachment: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(r, maxMailBytes+1))
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("outbox: read attachment: %w", err)
		}
		if total += len(data); total > maxMailBytes {
			return errors.New("outbox: attachments are too large")
		}
		args.Attachments = append(args.Attachments, mailer.Attachment{Filename: a.Filename, ContentType: a.ContentType, Content: data})
	}
	return nil
}
