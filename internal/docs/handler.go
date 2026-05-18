package docs

import (
	"embed"
	"fmt"
	"net/http"

	scalargo "github.com/bdpiprava/scalar-go"
	"github.com/go-chi/chi/v5"
)

//go:embed spec/swagger.json
var openAPISpec embed.FS

const developmentEnv = "development"

// IsEnabled reports whether interactive API docs should be served.
func IsEnabled(appEnv string) bool {
	return appEnv == developmentEnv
}

// RegisterRoutes mounts OpenAPI and Scalar documentation routes.
func RegisterRoutes(r chi.Router) {
	r.Get("/openapi.json", serveOpenAPI)
	r.Get("/docs", serveScalar)
}

func serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	content, err := openAPISpec.ReadFile("spec/swagger.json")
	if err != nil {
		http.Error(w, "openapi spec not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, writeErr := w.Write(content); writeErr != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func serveScalar(w http.ResponseWriter, r *http.Request) {
	html, err := scalargo.NewV2(
		scalargo.WithSpecURL(specURL(r)),
		scalargo.WithMetaDataOpts(
			scalargo.WithTitle("Angle HR Waitlist API"),
			scalargo.WithKeyValue("description", "Onboarding waitlist and admin API"),
		),
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("render docs: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, writeErr := w.Write([]byte(html)); writeErr != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func specURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}

	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = forwarded
	}

	return fmt.Sprintf("%s://%s/openapi.json", scheme, r.Host)
}
