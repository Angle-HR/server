package hiringhttp

import (
	"net/http"
	"strconv"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// AuditListEnvelope is a page of the company's job audit trail.
type AuditListEnvelope struct {
	Data []draft.AuditEvent `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// auditTrail godoc
//
//	@Summary		Job audit trail
//	@Description	The company's audit events, newest first: job changes written by hiring (source hiring) and application events written by the applications service (source applications: received, viewed, file_downloaded, status_changed). Needs the job.audit.view permission. Rows carry ids and the stored diff only, never candidate data. Page with next_cursor.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			job_id	query		string	false	"Only this job (the entity)"
//	@Param			actor_id	query		string	false	"Only events by this user id"
//	@Param			action	query		string	false	"Exact action, e.g. job.published"
//	@Param			from	query		string	false	"On or after this time (RFC 3339 or YYYY-MM-DD)"
//	@Param			to		query		string	false	"Before this time (RFC 3339), or up to the end of this day (YYYY-MM-DD)"
//	@Param			cursor	query		string	false	"next_cursor from the previous page"
//	@Param			limit	query		int		false	"Page size (default 50, max 100)"
//	@Success		200		{object}	hiringhttp.AuditListEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/audit [get]
func (h *HiringHandler) auditTrail(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	qv := r.URL.Query()
	q := draft.AuditQuery{
		JobID: qv.Get("job_id"), ActorID: qv.Get("actor_id"), Action: qv.Get("action"),
		From: qv.Get("from"), To: qv.Get("to"), Cursor: qv.Get("cursor"),
	}
	if raw := qv.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	items, next, err := svc.AuditTrail(r.Context(), c, q)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	more := next != ""
	response.SuccessWithMeta(w, r, http.StatusOK, nonNilAudit(items), &response.Meta{NextCursor: next, HasMore: &more})
}

func nonNilAudit(in []draft.AuditEvent) []draft.AuditEvent {
	if in == nil {
		return []draft.AuditEvent{}
	}
	return in
}
