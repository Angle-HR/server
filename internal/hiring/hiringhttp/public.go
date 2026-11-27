package hiringhttp

import (
	"errors"
	"net/http"
	"strconv"
	"time"

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

// RegisterCompanyRoutes mounts the careers page JSON, /public/companies/{slug}. It needs no sign-in.
func (h *PublicHandler) RegisterCompanyRoutes(r chi.Router) {
	if h.Remote {
		return
	}
	r.Get("/public/companies/{slug}", h.getCompany)
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

// PublicCompanyData is the careers page of one company.
type PublicCompanyData struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	About   string `json:"about"`
	Website string `json:"website,omitempty"`
	LogoURL string `json:"logo_url,omitempty"`
	// Teams are the departments with an open job, for the Team filter.
	Teams []string `json:"teams"`
	// Total counts the open jobs that match the filters, across all pages.
	Total      int                `json:"total"`
	Jobs       []PublicCompanyJob `json:"jobs"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

// PublicCompanyJob is one open job in the careers page list.
type PublicCompanyJob struct {
	PublicID       string `json:"public_id"`
	Title          string `json:"title"`
	Team           string `json:"team"`
	Location       string `json:"location,omitempty"`
	Workplace      string `json:"workplace_type,omitempty"`
	EmploymentType string `json:"employment_type,omitempty"`
	PublishedAt    string `json:"published_at"`
	ClosingDate    string `json:"closing_date,omitempty"`
	URL            string `json:"url"`
}

// PublicCompanyEnvelope is a successful careers page response.
type PublicCompanyEnvelope struct {
	Data PublicCompanyData `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// getCompany godoc
//
//	@Summary		Company careers page
//	@Description	The company's public profile and its open jobs, newest first, with filters and cursor paging. Only published jobs that are shown on the career page and are inside their closing date are listed. No sign-in.
//	@Tags			jobs
//	@Produce		json
//	@Param			slug			path		string	true	"Careers page address"
//	@Param			team			query		string	false	"Department name"
//	@Param			workplace_type	query		string	false	"onsite, hybrid or remote"
//	@Param			employment_type	query		string	false	"full_time, part_time, contract or internship"
//	@Param			location		query		string	false	"Text in the job's location"
//	@Param			q				query		string	false	"Text in the job title"
//	@Param			limit			query		int		false	"Page size, default 20, at most 50"
//	@Param			cursor			query		string	false	"next_cursor of the previous page"
//	@Success		200				{object}	hiringhttp.PublicCompanyEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Router			/public/companies/{slug} [get]
func (h *PublicHandler) getCompany(w http.ResponseWriter, r *http.Request) {
	qs := r.URL.Query()
	q := public.CompanyQuery{
		Team: qs.Get("team"), Workplace: qs.Get("workplace_type"), EmploymentType: qs.Get("employment_type"),
		Location: qs.Get("location"), Search: qs.Get("q"), Cursor: qs.Get("cursor"),
	}
	if v := qs.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	page, err := h.Public.Company(r.Context(), chi.URLParam(r, "slug"), q)
	switch {
	case errors.Is(err, public.ErrNotFound):
		err = apperror.New(apperror.CodeNotFound, "company not found")
	case errors.Is(err, public.ErrBadQuery):
		err = apperror.New(apperror.CodeValidationError, "the filters or the cursor are not valid")
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	out := PublicCompanyData{
		Slug: page.Company.Slug, Name: page.Company.Name, About: page.Company.About, Website: page.Company.Website,
		LogoURL: page.Company.LogoURL, Teams: page.Teams, Total: page.Total, NextCursor: page.NextCursor,
		Jobs: make([]PublicCompanyJob, 0, len(page.Jobs)),
	}
	for _, j := range page.Jobs {
		out.Jobs = append(out.Jobs, PublicCompanyJob{
			PublicID: j.PublicID, Title: j.Title, Team: j.Team, Location: j.Location, Workplace: j.Workplace,
			EmploymentType: j.EmploymentType, PublishedAt: j.PublishedAt.UTC().Format(time.RFC3339),
			ClosingDate: j.ClosingDate, URL: h.Public.JobURL(j.PublicID),
		})
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	response.Success(w, r, http.StatusOK, out)
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
