package waitlist

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Handler exposes waitlist HTTP endpoints.
type Handler struct {
	service *WaitlistService
}

// NewHandler returns an HTTP handler for waitlist routes.
func NewHandler(service *WaitlistService) *Handler {
	return &Handler{service: service}
}

// RegisterRoutes mounts waitlist routes on r.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Post("/waitlist", h.join)
	r.Get("/waitlist/count", h.count)
}

func (h *Handler) join(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, r, fmt.Errorf("decode request body: %w", apperror.ErrBadRequest))
		return
	}

	if err := h.service.Join(r.Context(), req); err != nil {
		response.Error(w, r, err)
		return
	}

	response.Success(w, r, http.StatusCreated, map[string]string{
		"message": "you're on the list",
	})
}

func (h *Handler) count(w http.ResponseWriter, r *http.Request) {
	count, err := h.service.Count(r.Context())
	if err != nil {
		response.Error(w, r, fmt.Errorf("count waitlist entries: %w", apperror.ErrInternal))
		return
	}

	response.Success(w, r, http.StatusOK, map[string]int64{
		"count": count,
	})
}
