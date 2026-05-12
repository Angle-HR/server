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

func (h *Handler) createNote(w http.ResponseWriter, r *http.Request) {
	publicID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidSubmissionID))
		return
	}

	var payload struct {
		Note      string `json:"note"`
		CreatedBy string `json:"created_by"`
	}
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
