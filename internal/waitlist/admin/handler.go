package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

type adminRepository interface {
	ListSubmissions(ctx context.Context, filter *ListFilter) (*ListResult, error)
	GetSubmission(ctx context.Context, publicID uuid.UUID) (*Detail, error)
	CreateNote(ctx context.Context, publicID uuid.UUID, note, createdBy string) (*Note, error)
	Stats(ctx context.Context) (*Stats, error)
}

// Handler exposes admin onboarding endpoints.
type Handler struct {
	repo adminRepository
}

// NewHandler returns an admin HTTP handler.
func NewHandler(repo adminRepository) *Handler {
	return &Handler{repo: repo}
}

// RegisterRoutes mounts admin routes on r.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/submissions", h.listSubmissions)
	r.Get("/submissions/{id}", h.getSubmission)
	r.Post("/submissions/{id}/notes", h.createNote)
	r.Get("/stats", h.stats)
}

// listSubmissions godoc
//
//	@Summary		List submissions
//	@Description	Returns a cursor-paginated list of waitlist submissions.
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Param			cursor				query		string	false	"Pagination cursor"
//	@Param			limit				query		int		false	"Page size (1-100)"
//	@Param			industry_id			query		string	false	"Filter by industry UUID"
//	@Param			role_id				query		string	false	"Filter by role UUID"
//	@Param			team_size_id		query		string	false	"Filter by team size UUID"
//	@Param			wants_early_access	query		bool	false	"Filter by early access opt-in"
//	@Param			wants_user_testing	query		bool	false	"Filter by user testing opt-in"
//	@Param			submitted_from		query		string	false	"Filter from timestamp (RFC3339)"
//	@Param			submitted_to		query		string	false	"Filter to timestamp (RFC3339)"
//	@Success		200					{object}	admin.SubmissionListEnvelope
//	@Failure		400					{object}	apidoc.ErrorEnvelope
//	@Failure		401					{object}	apidoc.ErrorEnvelope
//	@Failure		403					{object}	apidoc.ErrorEnvelope
//	@Failure		500					{object}	apidoc.ErrorEnvelope
//	@Router			/admin/submissions [get]
func (h *Handler) listSubmissions(w http.ResponseWriter, r *http.Request) {
	filter, err := parseListFilter(r)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	result, err := h.repo.ListSubmissions(r.Context(), &filter)
	if err != nil {
		response.Error(w, r, fmt.Errorf("list submissions: %w", err))
		return
	}

	hasMore := result.HasMore
	response.SuccessWithMeta(w, r, http.StatusOK, result.Items, &response.Meta{
		NextCursor: result.NextCursor,
		HasMore:    &hasMore,
	})
}

// getSubmission godoc
//
//	@Summary		Get submission
//	@Description	Returns a full waitlist submission by public ID.
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Submission public ID"
//	@Success		200	{object}	admin.SubmissionDetailEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/submissions/{id} [get]
func (h *Handler) getSubmission(w http.ResponseWriter, r *http.Request) {
	publicID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidSubmissionID))
		return
	}

	detail, err := h.repo.GetSubmission(r.Context(), publicID)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, apperror.MsgSubmissionNotFound))
		return
	}

	response.Success(w, r, http.StatusOK, detail)
}

// createNote godoc
//
//	@Summary		Create submission note
//	@Description	Adds an internal admin note to a submission.
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string					true	"Submission public ID"
//	@Param			body	body		admin.CreateNoteRequest	true	"Note payload"
//	@Success		201		{object}	admin.NoteEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/submissions/{id}/notes [post]
func (h *Handler) createNote(w http.ResponseWriter, r *http.Request) {
	publicID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidSubmissionID))
		return
	}

	var payload CreateNoteRequest
	if decodeErr := json.NewDecoder(r.Body).Decode(&payload); decodeErr != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	note := strings.TrimSpace(payload.Note)
	createdBy := strings.TrimSpace(payload.CreatedBy)
	if note == "" || createdBy == "" {
		response.Error(w, r, apperror.NewWithDetails(
			apperror.CodeValidationError,
			apperror.MsgNoteAndCreatedByRequired,
			map[string]any{
				"note":       note == "",
				"created_by": createdBy == "",
			},
		))
		return
	}

	created, err := h.repo.CreateNote(r.Context(), publicID, note, createdBy)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, apperror.MsgSubmissionNotFound))
		return
	}

	response.Success(w, r, http.StatusCreated, created)
}

// stats godoc
//
//	@Summary		Get submission stats
//	@Description	Returns aggregate waitlist submission statistics.
//	@Tags			admin
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	admin.StatsEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/stats [get]
func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.repo.Stats(r.Context())
	if err != nil {
		response.Error(w, r, fmt.Errorf("load stats: %w", err))
		return
	}

	response.Success(w, r, http.StatusOK, stats)
}

func parseListFilter(r *http.Request) (ListFilter, error) {
	filter := ListFilter{
		Cursor: r.URL.Query().Get("cursor"),
	}

	if err := applyListLimit(r, &filter); err != nil {
		return ListFilter{}, err
	}

	if err := applyListUUIDFilters(r, &filter); err != nil {
		return ListFilter{}, err
	}

	if err := applyListBoolFilters(r, &filter); err != nil {
		return ListFilter{}, err
	}

	if err := applyListTimeFilters(r, &filter); err != nil {
		return ListFilter{}, err
	}

	return filter, nil
}

func applyListLimit(r *http.Request, filter *ListFilter) error {
	limitValue := r.URL.Query().Get("limit")
	if limitValue == "" {
		return nil
	}

	limit, err := strconv.Atoi(limitValue)
	if err != nil || limit < 1 || limit > 100 {
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidLimit)
	}

	filter.Limit = limit
	return nil
}

func applyListUUIDFilters(r *http.Request, filter *ListFilter) error {
	if value := r.URL.Query().Get("industry_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidIndustryID)
		}

		filter.IndustryID = &id
	}

	if value := r.URL.Query().Get("role_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRoleID)
		}

		filter.RoleID = &id
	}

	if value := r.URL.Query().Get("team_size_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidTeamSizeID)
		}

		filter.TeamSizeID = &id
	}

	return nil
}

func applyListBoolFilters(r *http.Request, filter *ListFilter) error {
	if value := r.URL.Query().Get("wants_early_access"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidWantsEarlyAccess)
		}

		filter.WantsEarlyAccess = &parsed
	}

	if value := r.URL.Query().Get("wants_user_testing"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidWantsUserTesting)
		}

		filter.WantsUserTesting = &parsed
	}

	return nil
}

func applyListTimeFilters(r *http.Request, filter *ListFilter) error {
	if value := r.URL.Query().Get("submitted_from"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidSubmittedFrom)
		}

		filter.SubmittedFrom = &parsed
	}

	if value := r.URL.Query().Get("submitted_to"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidSubmittedTo)
		}

		filter.SubmittedTo = &parsed
	}

	return nil
}
