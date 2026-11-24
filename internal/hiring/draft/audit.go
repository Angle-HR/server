package draft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/rbac"
)

// Audit trail page sizes.
const (
	DefaultAuditLimit = 50
	MaxAuditLimit     = 100
)

// Audit event sources: the service that wrote the row.
const (
	AuditSourceHiring       = "hiring"
	AuditSourceApplications = "applications"
)

// AuditEvent is one row of a company's job audit trail. Diff is the stored JSON, passed through unchanged; rows
// carry ids only, never candidate data.
type AuditEvent struct {
	Source    string          `json:"source"`
	ID        int64           `json:"id"`
	JobID     string          `json:"job_id"`
	ActorID   *string         `json:"actor_id"`
	Action    string          `json:"action"`
	Diff      json.RawMessage `json:"diff"`
	CreatedAt time.Time       `json:"created_at"`
}

// AuditCursor is the position after the last row of a page: rows are ordered newest first by (created_at, source, id).
type AuditCursor struct {
	CreatedAt time.Time
	Source    string
	ID        int64
}

// AuditFilter narrows the trail. Empty/nil fields do not filter. From is inclusive, To exclusive.
type AuditFilter struct {
	JobID, ActorID, Action string
	From, To               *time.Time
	After                  *AuditCursor
	Limit                  int
}

// AuditReader reads the audit rows of one company, newest first (hiring.audit_events and hiring.app_audit_events).
type AuditReader interface {
	AuditEvents(ctx context.Context, tenantID string, f AuditFilter) ([]AuditEvent, error)
}

// AuditQuery is the request of Service.AuditTrail, with the raw strings of the HTTP query.
type AuditQuery struct {
	JobID, ActorID, Action string
	From, To               string // RFC 3339 or YYYY-MM-DD (To as a date means the end of that day)
	Cursor                 string
	Limit                  int
}

// ErrAuditUnavailable means this process has no audit reader.
var ErrAuditUnavailable = errors.New("draft: audit trail is not available")

// AuditTrail returns one page of the company's job audit trail. It needs rbac.JobAudit.
func (s *Service) AuditTrail(ctx context.Context, c Caller, q AuditQuery) ([]AuditEvent, string, error) {
	if !c.can(rbac.JobAudit) {
		return nil, "", forbidden("you do not have permission to view the audit trail")
	}
	if s.Audit == nil {
		return nil, "", ErrAuditUnavailable
	}
	var errs []jobs.FieldError
	f := AuditFilter{JobID: strings.TrimSpace(q.JobID), ActorID: strings.TrimSpace(q.ActorID), Action: strings.TrimSpace(q.Action)}
	if f.JobID != "" {
		if _, err := uuid.Parse(f.JobID); err != nil {
			errs = append(errs, fe("job_id", "must be a job id"))
		}
	}
	if f.ActorID != "" {
		if _, err := uuid.Parse(f.ActorID); err != nil {
			errs = append(errs, fe("actor_id", "must be a user id"))
		}
	}
	var err error
	if f.From, err = parseAuditTime(q.From, false); err != nil {
		errs = append(errs, fe("from", "must be an RFC 3339 time or a YYYY-MM-DD date"))
	}
	if f.To, err = parseAuditTime(q.To, true); err != nil {
		errs = append(errs, fe("to", "must be an RFC 3339 time or a YYYY-MM-DD date"))
	}
	if f.From != nil && f.To != nil && !f.From.Before(*f.To) {
		errs = append(errs, fe("to", "must be after from"))
	}
	if q.Cursor != "" {
		if f.After, err = DecodeAuditCursor(q.Cursor); err != nil {
			errs = append(errs, fe("cursor", "is not a cursor from a previous page"))
		}
	}
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultAuditLimit
	case limit < 0 || limit > MaxAuditLimit:
		errs = append(errs, fe("limit", "must be between 1 and 100"))
	}
	if len(errs) > 0 {
		return nil, "", invalid(errs...)
	}
	f.Limit = limit + 1
	rows, err := s.Audit.AuditEvents(ctx, c.OrgID, f)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = EncodeAuditCursor(AuditCursor{CreatedAt: last.CreatedAt, Source: last.Source, ID: last.ID})
	}
	return rows, next, nil
}

func parseAuditTime(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		t = t.UTC()
		return &t, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		t = t.AddDate(0, 0, 1) // the whole day is included: To is exclusive
	}
	return &t, nil
}

// EncodeAuditCursor and DecodeAuditCursor turn a page position into an opaque string and back.
func EncodeAuditCursor(c AuditCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.Source + "|" + strconv.FormatInt(c.ID, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeAuditCursor parses what EncodeAuditCursor made.
func DecodeAuditCursor(s string) (*AuditCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(b), "|")
	if len(parts) != 3 || (parts[1] != AuditSourceHiring && parts[1] != AuditSourceApplications) {
		return nil, errors.New("bad audit cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, err
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, err
	}
	return &AuditCursor{CreatedAt: t, Source: parts[1], ID: id}, nil
}
