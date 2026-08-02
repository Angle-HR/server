package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	fluvio "github.com/software78/fluvio"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type adminJobView struct {
	ID          int64           `json:"id"`
	Queue       string          `json:"queue"`
	Kind        string          `json:"kind"`
	Args        json.RawMessage `json:"args"`
	State       string          `json:"state"`
	Priority    int16           `json:"priority"`
	Attempt     int16           `json:"attempt"`
	MaxAttempts int16           `json:"max_attempts"`
	AttemptedBy []string        `json:"attempted_by"`
	ScheduledAt time.Time       `json:"scheduled_at"`
	AttemptedAt *time.Time      `json:"attempted_at,omitempty"`
	FinalizedAt *time.Time      `json:"finalized_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	ErrorTrace  json.RawMessage `json:"error_trace,omitempty"`
	Tags        []string        `json:"tags"`
	UniqueKey   *string         `json:"unique_key,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
}

func jobView(row fluvio.JobRow) adminJobView {
	return adminJobView{
		ID:          row.ID,
		Queue:       row.Queue,
		Kind:        row.Kind,
		Args:        row.Args,
		State:       string(row.State),
		Priority:    row.Priority,
		Attempt:     row.Attempt,
		MaxAttempts: row.MaxAttempts,
		AttemptedBy: row.AttemptedBy,
		ScheduledAt: row.ScheduledAt,
		AttemptedAt: row.AttemptedAt,
		FinalizedAt: row.FinalizedAt,
		CreatedAt:   row.CreatedAt,
		ErrorTrace:  row.ErrorTrace,
		Tags:        row.Tags,
		UniqueKey:   row.UniqueKey,
		Metadata:    row.Metadata,
	}
}

// listJobs godoc
//
//	@Summary		List Fluvio jobs
//	@Tags			admin/jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			queue	query	string	false	"Queue name"
//	@Param			state	query	string	false	"Job state"
//	@Param			kind	query	string	false	"Job kind"
//	@Param			limit	query	int		false	"Page size"
//	@Param			offset	query	int		false	"Offset"
//	@Success		200		{object}	handler.AdminJobListEnvelope
//	@Router			/admin/jobs [get]
func (h *AdminHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	if h.Jobs == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	queue := r.URL.Query().Get("queue")
	state := r.URL.Query().Get("state")
	kind := r.URL.Query().Get("kind")
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 100)
	offset := parseLimit(r.URL.Query().Get("offset"), 0, 100000)
	if r.URL.Query().Get("offset") == "" {
		offset = 0
	}

	rows, err := h.Jobs.ListJobs(r.Context(), queue, state, kind, limit, offset)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, err.Error()))
		return
	}
	out := make([]adminJobView, 0, len(rows))
	for _, row := range rows {
		out = append(out, jobView(row))
	}
	response.Success(w, r, http.StatusOK, out)
}

// getJob godoc
//
//	@Summary		Get Fluvio job
//	@Tags			admin/jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		int	true	"Job ID"
//	@Success		200	{object}	handler.AdminJobEnvelope
//	@Router			/admin/jobs/{id} [get]
func (h *AdminHandler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid job id"))
		return
	}
	row, err := h.Jobs.GetJob(r.Context(), id)
	if err != nil {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}
	response.Success(w, r, http.StatusOK, jobView(*row))
}

// retryJob godoc
//
//	@Summary		Retry Fluvio job
//	@Description	Replays dead jobs, runs scheduled jobs now, or requeues failed jobs as pending.
//	@Tags			admin/jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		int	true	"Job ID"
//	@Success		200	{object}	handler.AdminJobEnvelope
//	@Router			/admin/jobs/{id}/retry [post]
func (h *AdminHandler) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid job id"))
		return
	}

	row, err := h.Jobs.GetJob(r.Context(), id)
	if err != nil {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	switch row.State {
	case fluvio.JobStateDead:
		if err := h.Jobs.ReplayDeadJob(r.Context(), id); err != nil {
			response.Error(w, r, err)
			return
		}
	case fluvio.JobStateScheduled:
		if err := h.Jobs.RunJobNow(r.Context(), id); err != nil {
			response.Error(w, r, err)
			return
		}
	case fluvio.JobStateFailed:
		_, err := h.GlobalDB.Exec(r.Context(), `
			UPDATE fluvio_jobs
			SET state = 'pending', scheduled_at = now(), finalized_at = NULL, error_trace = NULL
			WHERE id = $1 AND state = 'failed'
		`, id)
		if err != nil {
			response.Error(w, r, err)
			return
		}
	default:
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "job state cannot be retried"))
		return
	}

	h.audit(r, "jobs.retry", "job", strconv.FormatInt(id, 10), map[string]any{"previous_state": string(row.State)})

	updated, err := h.Jobs.GetJob(r.Context(), id)
	if err != nil {
		response.Success(w, r, http.StatusOK, map[string]any{"id": id, "retried": true})
		return
	}
	response.Success(w, r, http.StatusOK, jobView(*updated))
}
