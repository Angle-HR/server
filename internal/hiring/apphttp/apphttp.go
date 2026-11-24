// Package apphttp serves the HTTP API of the applications side: public apply and its upload endpoint, the
// company-side applications routes (list, open, change status, decide, files) and the candidate settings.
//
// One set of handlers is hosted in two places: the monolith (hiringhttp mounts the pieces inside its own
// routers, so the routes stay exactly the same) and the applications service (cmd/applications calls Mount).
// The package depends on the applications side only (applications, apply, uploads, appjobs and pkg/*), never on
// a hiring package; see the import rule in appjobs/imports_test.go.
//
// Who is calling is a CallerResolver, so the host decides where company and role data come from.
package apphttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Identify reads the signed-in user from the request context (set by the host's auth middleware).
type Identify func(ctx context.Context) (userID string, reg region.Region, ok bool)

// RegionalApplications returns the applications service of a region.
type RegionalApplications func(reg region.Region) (*applications.Service, error)

// Deps is everything a host supplies.
type Deps struct {
	// Identify reads the authenticated user. Required for the company-side routes.
	Identify Identify
	// Callers resolves the user into the company caller. Required for the company-side routes.
	Callers CallerResolver
	// Applications returns a region's applications service. Nil leaves the company-side routes answering 503.
	Applications RegionalApplications
	// Apply takes public applications. Required for the apply route.
	Apply *apply.Service
	// Uploads issues upload URLs. Nil when uploads are not configured (no scanner): forms that ask for a file
	// then cannot be answered.
	Uploads *uploads.Service
	// RequireAuth wraps the company-side routes in Mount (session authentication). Nil mounts them unwrapped,
	// for a host that authenticates further up. Hosts that mount the pieces themselves ignore it.
	RequireAuth func(http.Handler) http.Handler
}

// Handler serves the routes over Deps.
type Handler struct{ Deps }

// New returns a handler over d.
func New(d Deps) *Handler { return &Handler{Deps: d} }

// Mount registers every route on r, which is the /api/v1 router:
//
//	POST /public/jobs/{public_id}/applications      no sign-in
//	POST /public/jobs/{public_id}/uploads           no sign-in
//	GET  /jobs/{id}/applications[/{app_id}[/files/{file_id}]], POST /jobs/{id}/applications/{app_id}/(status|review)
//	GET|PUT /hiring/applicant-settings              behind d.RequireAuth
//
// The monolith does not call Mount: /jobs and /hiring also carry hiring routes there, and chi cannot mount two
// routers on one prefix, so hiringhttp mounts RegisterPublicRoutes, RegisterJobRoutes and RegisterSettingsRoutes
// inside its own.
func Mount(r chi.Router, d Deps) { New(d).Mount(r) }

// Mount is the method form of Mount.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/public/jobs", h.RegisterPublicRoutes)
	r.Group(func(r chi.Router) {
		if h.RequireAuth != nil {
			r.Use(h.RequireAuth)
		}
		r.Route("/jobs/{id}", h.RegisterJobRoutes)
		r.Route("/hiring", h.RegisterSettingsRoutes)
	})
}

const maxBodyBytes = 1 << 20

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.Error(w, r, apperror.New(apperror.CodePayloadTooLarge, apperror.MsgRequestBodyTooLarge))
		} else {
			response.Error(w, r, apperror.InvalidBody(err))
		}
		return nil, false
	}
	return raw, true
}

func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
