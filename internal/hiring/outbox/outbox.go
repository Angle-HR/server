// Package outbox delivers the emails the hiring code leaves in a region's database: the confirmation to a
// candidate, the notice that their application was decided, and the note to a company about deleted applications.
// They are kept in the region, not in the global job queue, because they hold a candidate's name and address.
//
// The worker leases a due email (so two runs cannot send it twice at the same moment), sends it, and then marks
// it sent or schedules a retry with a growing delay. After MaxAttempts it is marked failed and left for the
// retention task to delete.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/mailer"
)

// Email is a leased outbox row.
type Email struct {
	ID        string
	Kind      string
	Recipient string
	Name      string
	Data      json.RawMessage
	Attempts  int // including this one
}

// Store is one region's database as the sender uses it.
type Store interface {
	// Lease returns up to limit due emails and hides them from other runs for the lease time.
	Lease(ctx context.Context, tenantID string, now time.Time, limit int, lease time.Duration) ([]Email, error)
	MarkSent(ctx context.Context, tenantID, id string, now time.Time) error
	// MarkFailed records a failed attempt. With final true the email is given up on; otherwise it is retried at
	// next.
	MarkFailed(ctx context.Context, tenantID, id, reason string, final bool, next time.Time) error
}

// Sender delivers one email. *mailer.Mailer implements it.
type Sender interface {
	Send(ctx context.Context, args mailer.EmailArgs) error
}

// Defaults.
const (
	MaxAttempts = 5
	leaseTime   = 10 * time.Minute
	batchSize   = 50
	// maxPerCompany bounds one run, so one company with a large backlog cannot starve the others.
	maxPerCompany = 500
)

var backoff = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// Worker sends the emails of a region.
type Worker struct {
	Tenants     func(ctx context.Context) ([]string, error)
	Store       Store
	Sender      Sender
	Now         func() time.Time
	Logger      *slog.Logger
	MaxAttempts int // 0 uses MaxAttempts
}

// Result counts what a run did.
type Result struct{ Sent, Retry, Failed int }

// Run sends every due email once.
func (w *Worker) Run(ctx context.Context) (Result, error) {
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
			list, err := w.Store.Lease(ctx, tid, now(), batchSize, leaseTime)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("outbox: lease emails: %w", err)
				}
				break
			}
			for _, e := range list {
				done++
				args, err := Args(e)
				if err == nil {
					err = w.Sender.Send(ctx, args)
				}
				if err == nil {
					if err = w.Store.MarkSent(ctx, tid, e.ID, now()); err != nil && w.Logger != nil {
						w.Logger.Error("outbox: mark sent", "error", err)
					}
					res.Sent++
					continue
				}
				// A template or data problem will not fix itself on retry, but the cap bounds the retries.
				final := e.Attempts >= max
				next := now().Add(delay(e.Attempts))
				if merr := w.Store.MarkFailed(ctx, tid, e.ID, truncate(err.Error(), 300), final, next); merr != nil && w.Logger != nil {
					w.Logger.Error("outbox: mark failed", "error", merr)
				}
				if final {
					res.Failed++
				} else {
					res.Retry++
				}
				if w.Logger != nil {
					w.Logger.Warn("outbox: email not sent", "kind", e.Kind, "attempt", e.Attempts, "final", final)
				}
			}
			if len(list) < batchSize {
				break
			}
		}
	}
	return res, firstErr
}

func delay(attempts int) time.Duration {
	i := attempts - 1
	if i < 0 {
		i = 0
	}
	if i >= len(backoff) {
		i = len(backoff) - 1
	}
	return backoff[i]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

type data struct {
	JobTitle  string `json:"job_title"`
	JobCode   string `json:"job_code"`
	Company   string `json:"company"`
	NoticeURL string `json:"notice_url"`
	Items     []struct {
		JobCode  string `json:"job_code"`
		JobTitle string `json:"job_title"`
		Count    int    `json:"count"`
	} `json:"items"`
}

// Args turns an outbox row into the arguments of the mailer.
func Args(e Email) (mailer.EmailArgs, error) {
	var d data
	if len(e.Data) > 0 {
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return mailer.EmailArgs{}, fmt.Errorf("outbox: email data: %w", err)
		}
	}
	a := mailer.EmailArgs{
		Recipient: e.Recipient, FullName: e.Name, OrganizationName: d.Company,
		JobTitle: d.JobTitle, JobCode: d.JobCode, NoticeURL: d.NoticeURL,
	}
	switch e.Kind {
	case "application_received":
		a.Type = mailer.TypeApplicationReceived
		a.Subject = "We received your application for " + d.JobTitle
	case "application_rejected":
		a.Type = mailer.TypeApplicationRejected
		a.Subject = "An update on your application for " + d.JobTitle
	case "application_hired":
		a.Type = mailer.TypeApplicationHired
		a.Subject = "Good news about your application for " + d.JobTitle
	case "retention_notice":
		a.Type = mailer.TypeRetentionNotice
		for _, it := range d.Items {
			a.Count += it.Count
			word := "applications"
			if it.Count == 1 {
				word = "application"
			}
			a.Lines = append(a.Lines, strings.TrimSpace(fmt.Sprintf("%s %s: %d %s", it.JobCode, it.JobTitle, it.Count, word)))
		}
		a.Subject = ""
	default:
		return mailer.EmailArgs{}, errors.New("outbox: unknown email kind " + e.Kind)
	}
	if strings.TrimSpace(a.Recipient) == "" {
		return mailer.EmailArgs{}, errors.New("outbox: email has no recipient")
	}
	return a, nil
}
