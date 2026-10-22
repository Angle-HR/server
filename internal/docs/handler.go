package docs

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	scalargo "github.com/bdpiprava/scalar-go"
	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/pkg/config"
)

//go:embed spec/swagger.json
var openAPISpec embed.FS

const productionEnv = "production"

// The docs are also served under /api, the only prefix the CDN in front of staging forwards to the server (the
// site root belongs to the frontend). That page embeds the spec instead of fetching it, so it works on whatever host
// it is opened from (the CDN, a tunnel, localhost): Scalar only accepts absolute spec URLs, and behind the CDN the
// request's Host is the origin, not the public name.
const (
	APIDocsPath = "/api/docs"
	APISpecPath = "/api/openapi.json"
)

// Config holds runtime settings for API documentation routes.
type Config struct {
	PublicAPIURL string
}

// IsEnabled reports whether interactive API docs should be served.
func IsEnabled(appEnv string) bool {
	return appEnv != productionEnv
}

// RegisterRoutes mounts OpenAPI and Scalar documentation routes.
func RegisterRoutes(r chi.Router, cfg Config) {
	origin := config.NormalizePublicAPIURL(cfg.PublicAPIURL)
	parsed, err := url.Parse(origin)
	if err != nil {
		panic(fmt.Sprintf("docs: invalid PUBLIC_API_URL %q: %v", cfg.PublicAPIURL, err))
	}

	handler := &docHandler{
		specURL: specURL(origin),
		host:    parsed.Host,
		scheme:  parsed.Scheme,
	}

	r.Get("/openapi.json", handler.serveOpenAPI)
	r.Get("/", handler.serveScalar)
	r.Get(APISpecPath, handler.serveOpenAPI)
	r.Get(APIDocsPath, handler.serveScalarEmbedded)
}

type docHandler struct {
	specURL string
	host    string
	scheme  string

	patchedOnce sync.Once
	patchedSpec []byte
	patchedErr  error
}

func (h *docHandler) patchedOpenAPISpec() ([]byte, error) {
	h.patchedOnce.Do(func() {
		content, err := openAPISpec.ReadFile("spec/swagger.json")
		if err != nil {
			h.patchedErr = fmt.Errorf("read openapi spec: %w", err)
			return
		}

		h.patchedSpec, h.patchedErr = patchOpenAPISpec(content, h.host, h.scheme)
	})

	return h.patchedSpec, h.patchedErr
}

func (h *docHandler) serveOpenAPI(w http.ResponseWriter, _ *http.Request) {
	content, err := h.patchedOpenAPISpec()
	if err != nil {
		http.Error(w, "openapi spec not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if _, writeErr := w.Write(content); writeErr != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (h *docHandler) serveScalar(w http.ResponseWriter, _ *http.Request) {
	h.renderScalar(w, scalargo.WithSpecURL(h.specURL))
}

// serveScalarEmbedded renders the reference page with the spec inlined (see APIDocsPath).
func (h *docHandler) serveScalarEmbedded(w http.ResponseWriter, _ *http.Request) {
	content, err := h.patchedOpenAPISpec()
	if err != nil {
		http.Error(w, "openapi spec not found", http.StatusInternalServerError)
		return
	}
	h.renderScalar(w, scalargo.WithSpecBytes(content))
}

func (h *docHandler) renderScalar(w http.ResponseWriter, spec scalargo.Option) {
	html, err := scalargo.NewV2(
		spec,
		scalargo.WithMetaDataOpts(
			scalargo.WithTitle("Angle HR API"),
			scalargo.WithKeyValue("description", "Waitlist and product onboarding API"),
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

func patchOpenAPISpec(content []byte, host, scheme string) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal openapi spec: %w", err)
	}

	doc["host"] = host
	switch scheme {
	case "http", "https":
		doc["schemes"] = []string{scheme}
	default:
		return nil, fmt.Errorf(
			"patchOpenAPISpec: unsupported scheme %q (expected http or https); check PUBLIC_API_URL",
			scheme,
		)
	}
	ApplyAPITagGroups(doc)

	patched, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("marshal openapi spec: %w", err)
	}

	return patched, nil
}

// specURL builds the OpenAPI document URL for Scalar from a public API origin.
func specURL(origin string) string {
	return strings.TrimRight(origin, "/") + "/openapi.json"
}
