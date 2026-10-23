package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/pkg/besteffort"
)

// HealthPath is the liveness endpoint polled by the load balancer and the container health check.
const HealthPath = "/healthz"

// HealthHandler answers liveness probes. It checks nothing downstream, so a database or Redis outage does not make
// the orchestrator restart healthy tasks; a process that can answer is alive.
type HealthHandler struct{}

// NewHealthHandler returns a health handler.
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// RegisterRoutes mounts the health route on r. It is deliberately outside /api/v1 and not documented in the API spec.
func (h *HealthHandler) RegisterRoutes(r chi.Router) {
	r.Get(HealthPath, h.get)
	r.Head(HealthPath, h.get)
}

func (h *HealthHandler) get(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(`{"status":"ok"}`))
	besteffort.Log(r.Context(), "health write", err) // the probe may have hung up; nothing to recover
}
