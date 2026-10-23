package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHealthHandler(t *testing.T) {
	t.Parallel()

	router := chi.NewRouter()
	NewHealthHandler().RegisterRoutes(router)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequestWithContext(t.Context(), method, HealthPath, http.NoBody)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, want 200", method, HealthPath, rec.Code)
		}
		if method == http.MethodGet && rec.Body.String() != `{"status":"ok"}` {
			t.Fatalf("body = %q", rec.Body.String())
		}
	}
}
