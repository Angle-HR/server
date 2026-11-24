package hiringhttp

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/appstore"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// PublicHandler serves what candidates reach without signing in: the job page, its privacy notice, the sitemap,
// the JSON the apply page is built from, and the application itself.
type PublicHandler struct {
	Public *public.Service
	Apply  *apply.Service
	// Uploads issues upload URLs. Nil when uploads are not configured (no scanner): forms that ask for a file
	// then cannot be answered.
	Uploads *uploads.Service
	// Remote leaves the careers page, the sitemap and the public job JSON to the hiring service (HIRING_REMOTE=on
	// on the monolith). The apply routes are unaffected.
	Remote bool
	// NoApplications is set by the hiring service: the apply and upload routes belong to the applications service.
	NoApplications bool
}

// NewPublicHandler returns a handler over the public job service and the application service.
func NewPublicHandler(p *public.Service, a *apply.Service) *PublicHandler {
	return &PublicHandler{Public: p, Apply: a}
}

// PublicDirectory finds the store of a region for the public job service.
type PublicDirectory struct {
	Pool RegionalDB
	// Org, when set, supplies the company row of the page (ORG_COMPANY=grpc).
	Org hiringstore.CompanyOrg
}

// Regional implements public.Directory.
func (d PublicDirectory) Regional(reg string) (public.Regional, error) {
	pool, err := d.Pool(region.Region(reg))
	if err != nil {
		return nil, err
	}
	return &hiringstore.Store{DB: pool, Region: reg, Org: d.Org}, nil
}

// ApplyDirectory finds the store of a region for the application service.
type ApplyDirectory struct{ Pool RegionalDB }

// Store implements apply.Directory.
func (d ApplyDirectory) Store(reg string) (apply.Store, error) {
	pool, err := d.Pool(region.Region(reg))
	if err != nil {
		return nil, err
	}
	return &appstore.Store{DB: pool, Region: reg}, nil
}

// UploadDirectory finds the database and the bucket of a region for the upload step.
type UploadDirectory struct {
	Pool   RegionalDB
	Bucket func(region string) (uploads.Storage, error)
}

// Store implements uploads.Directory.
func (d UploadDirectory) Store(reg string) (uploads.Store, error) {
	pool, err := d.Pool(region.Region(reg))
	if err != nil {
		return nil, err
	}
	return &appstore.Store{DB: pool}, nil
}

// Storage implements uploads.Directory.
func (d UploadDirectory) Storage(reg string) (uploads.Storage, error) { return d.Bucket(reg) }

// Compile-time checks that the real stores satisfy the public and apply interfaces.
var (
	_ public.Registry    = (*hiringstore.Global)(nil)
	_ public.Catalog     = (*hiringstore.Global)(nil)
	_ public.Regional    = (*hiringstore.Store)(nil)
	_ apply.Store        = (*appstore.Store)(nil)
	_ public.Directory   = PublicDirectory{}
	_ apply.Directory    = ApplyDirectory{}
	_ uploads.Store      = (*appstore.Store)(nil)
	_ uploads.SweepStore = (*appstore.Store)(nil)
	_ uploads.Directory  = UploadDirectory{}
)

// RegisterRoutes mounts the JSON routes under /api/v1. None of them needs sign-in.
func (h *PublicHandler) RegisterRoutes(r chi.Router) {
	r.Route("/public/jobs", func(r chi.Router) {
		if !h.Remote {
			r.Get("/sitemap.xml", h.sitemap)
			r.Get("/{public_id}", h.getJob)
		}
		if !h.NoApplications && !applicationsRemote() {
			apphttp.New(apphttp.Deps{Apply: h.Apply, Uploads: h.Uploads}).RegisterPublicRoutes(r)
		}
	})
}

// RegisterPages mounts the server-rendered pages at the site root. They are separate from the JSON API because
// link previews and Google read them as HTML.
func (h *PublicHandler) RegisterPages(r chi.Router) {
	if h.Remote {
		return
	}
	r.Get("/j/{public_id}", h.page)
	r.Get("/j/{public_id}/privacy", h.notice)
}

// PublicJobData is what the apply page is built from.
type PublicJobData struct {
	PublicID         string         `json:"public_id"`
	State            string         `json:"state"`
	Accepting        bool           `json:"accepting"`
	Job              jobs.PublicJob `json:"job"`
	Markets          []string       `json:"markets"`
	PrivacyContact   string         `json:"privacy_contact_email,omitempty"`
	PrivacyNoticeURL string         `json:"privacy_notice_url"`
	NoticeVersion    string         `json:"privacy_notice_version"`
	ConsentRequired  bool           `json:"consent_required"`
	URL              string         `json:"url"`
}

// PublicJobEnvelope is a successful public job response.
type PublicJobEnvelope struct {
	Data PublicJobData `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

// getJob godoc
//
//	@Summary		Public job
//	@Description	The job and its application form as a candidate sees it. No sign-in. Knockout rules, the lawful basis and the assessment link are never included. Closed, paused and archived jobs say so in state.
//	@Tags			jobs
//	@Produce		json
//	@Param			public_id	path		string	true	"Public job id"
//	@Success		200			{object}	hiringhttp.PublicJobEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		410			{object}	apidoc.ErrorEnvelope
//	@Router			/public/jobs/{public_id} [get]
func (h *PublicHandler) getJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "public_id")
	v, err := h.Public.Load(r.Context(), id)
	if err != nil {
		response.Error(w, r, publicError(err))
		return
	}
	data := PublicJobData{
		PublicID:         v.PublicID,
		State:            string(v.State),
		Accepting:        v.Accepting(),
		Job:              v.Job,
		Markets:          append([]string{}, v.Markets...),
		PrivacyContact:   v.Company.PrivacyEmail,
		PrivacyNoticeURL: h.Public.JobURL(id) + "/privacy",
		NoticeVersion:    public.NoticeVersion,
		ConsentRequired:  v.LawfulBasis == "consent",
		URL:              h.Public.JobURL(id),
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	response.Success(w, r, http.StatusOK, data)
}

// sitemap serves the jobs search engines may index.
func (h *PublicHandler) sitemap(w http.ResponseWriter, r *http.Request) {
	list, err := h.Public.Sitemap(r.Context(), public.MaxSitemapURLs)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	body, err := public.RenderSitemap(h.Public.SiteURL, list)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// page serves /j/{public_id}: the job as HTML with link-preview tags and Google for Jobs markup.
func (h *PublicHandler) page(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "public_id")
	v, err := h.Public.Load(r.Context(), id)
	if err != nil {
		h.pageError(w, r, err)
		return
	}
	src := r.URL.Query().Get("src")
	if !public.ValidSource(src) {
		src = ""
	}
	body, err := public.RenderPage(v, h.Public.JobURL(id), h.Public.ApplyLink(id, src), h.Public.JobURL(id)+"/privacy")
	if err != nil {
		response.Error(w, r, err)
		return
	}
	writeHTML(w, http.StatusOK, body, "public, max-age=300")
}

// notice serves /j/{public_id}/privacy: the applicant privacy notice generated from the job.
func (h *PublicHandler) notice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "public_id")
	v, err := h.Public.Load(r.Context(), id)
	if err != nil {
		h.pageError(w, r, err)
		return
	}
	body, err := public.RenderNotice(v, h.Public.JobURL(id))
	if err != nil {
		response.Error(w, r, err)
		return
	}
	writeHTML(w, http.StatusOK, body, "public, max-age=300")
}

// pageError answers a page request for a job that cannot be shown. People who follow an old link get a plain
// page; an unexpected failure uses the JSON error envelope, which also logs it.
func (h *PublicHandler) pageError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, public.ErrNotFound):
		writeHTML(w, http.StatusNotFound, public.RenderMessage("Job not found",
			"This job does not exist, or the link is wrong."), "public, max-age=60")
	case errors.Is(err, public.ErrGone):
		writeHTML(w, http.StatusGone, public.RenderMessage("This job is no longer available",
			"The employer has removed this job."), "public, max-age=300")
	default:
		response.Error(w, r, err)
	}
}

// writeHTML writes a page with headers that keep it inert: it loads nothing and cannot be framed.
func writeHTML(w http.ResponseWriter, status int, body []byte, cache string) {
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", cache)
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	hd.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// publicError maps a public job error to an API error.
func publicError(err error) error {
	switch {
	case errors.Is(err, public.ErrNotFound):
		return apperror.New(apperror.CodeNotFound, "job not found")
	case errors.Is(err, public.ErrGone):
		return apperror.New(apperror.CodeGone, "this job is no longer available")
	}
	return err
}
