package hiringhttp

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/apply"
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
}

// NewPublicHandler returns a handler over the public job service and the application service.
func NewPublicHandler(p *public.Service, a *apply.Service) *PublicHandler {
	return &PublicHandler{Public: p, Apply: a}
}

// PublicDirectory finds the store of a region for the public job service.
type PublicDirectory struct{ Pool RegionalDB }

// Regional implements public.Directory.
func (d PublicDirectory) Regional(reg string) (public.Regional, error) {
	pool, err := d.Pool(region.Region(reg))
	if err != nil {
		return nil, err
	}
	return &hiringstore.Store{DB: pool}, nil
}

// ApplyDirectory finds the store of a region for the application service.
type ApplyDirectory struct{ Pool RegionalDB }

// Store implements apply.Directory.
func (d ApplyDirectory) Store(reg string) (apply.Store, error) {
	pool, err := d.Pool(region.Region(reg))
	if err != nil {
		return nil, err
	}
	return &hiringstore.Store{DB: pool}, nil
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
	return &hiringstore.Store{DB: pool}, nil
}

// Storage implements uploads.Directory.
func (d UploadDirectory) Storage(reg string) (uploads.Storage, error) { return d.Bucket(reg) }

// Compile-time checks that the real stores satisfy the public and apply interfaces.
var (
	_ public.Registry    = (*hiringstore.Global)(nil)
	_ public.Catalog     = (*hiringstore.Global)(nil)
	_ public.Regional    = (*hiringstore.Store)(nil)
	_ apply.Store        = (*hiringstore.Store)(nil)
	_ public.Directory   = PublicDirectory{}
	_ apply.Directory    = ApplyDirectory{}
	_ uploads.Store      = (*hiringstore.Store)(nil)
	_ uploads.SweepStore = (*hiringstore.Store)(nil)
	_ uploads.Directory  = UploadDirectory{}
)

// RegisterRoutes mounts the JSON routes under /api/v1. None of them needs sign-in.
func (h *PublicHandler) RegisterRoutes(r chi.Router) {
	r.Route("/public/jobs", func(r chi.Router) {
		r.Get("/sitemap.xml", h.sitemap)
		r.Get("/{public_id}", h.getJob)
		r.Post("/{public_id}/uploads", h.presign)
		r.Post("/{public_id}/applications", h.submit)
	})
}

// RegisterPages mounts the server-rendered pages at the site root. They are separate from the JSON API because
// link previews and Google read them as HTML.
func (h *PublicHandler) RegisterPages(r chi.Router) {
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

// ApplicationRequest is what a candidate sends.
type ApplicationRequest struct {
	// CandidateCountry is the candidate's country, ISO 3166-1 alpha-2. It must belong to one of the job's markets.
	CandidateCountry string `json:"candidate_country"`
	// Answers maps a question id to the answer, in the shape that question's type expects.
	Answers                   map[string]json.RawMessage `json:"answers"`
	PrivacyNoticeAcknowledged bool                       `json:"privacy_notice_acknowledged"`
	// Consent is required only when the job's lawful basis is consent (see consent_required).
	Consent bool `json:"consent"`
	// Source is the share channel the candidate came from (the src of the link); anything else is "direct".
	Source string `json:"source"`
}

// ApplicationReceipt is the same for a new application and a repeat, so it cannot be used to find out whether
// an email has applied.
type ApplicationReceipt struct {
	Received bool `json:"received"`
}

// ApplicationReceiptEnvelope is a successful application response.
type ApplicationReceiptEnvelope struct {
	Data ApplicationReceipt `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
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

// submit godoc
//
//	@Summary		Apply for a job
//	@Description	Sends an application. No sign-in. The answers are checked against the form that was frozen when the job was published. A failed knockout or screening rule flags the application for a person to review; it never rejects it. Applying again with the same email returns the same success and changes nothing. Rate limited.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Param			public_id			path		string							true	"Public job id"
//	@Param			Idempotency-Key		header		string							false	"A random 8 to 64 character key; a retry with the same key never creates a second application"
//	@Param			body				body		hiringhttp.ApplicationRequest	true	"Application"
//	@Success		201					{object}	hiringhttp.ApplicationReceiptEnvelope
//	@Failure		400					{object}	apidoc.ErrorEnvelope
//	@Failure		404					{object}	apidoc.ErrorEnvelope
//	@Failure		409					{object}	apidoc.ErrorEnvelope
//	@Failure		410					{object}	apidoc.ErrorEnvelope
//	@Failure		429					{object}	apidoc.ErrorEnvelope
//	@Failure		503					{object}	apidoc.ErrorEnvelope
//	@Router			/public/jobs/{public_id}/applications [post]
func (h *PublicHandler) submit(w http.ResponseWriter, r *http.Request) {
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req ApplicationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	rec, err := h.Apply.Submit(r.Context(), apply.Input{
		PublicID:           chi.URLParam(r, "public_id"),
		CandidateCountry:   req.CandidateCountry,
		Answers:            req.Answers,
		NoticeAcknowledged: req.PrivacyNoticeAcknowledged,
		Consent:            req.Consent,
		Source:             req.Source,
		IdempotencyKey:     r.Header.Get("Idempotency-Key"),
		ClientIP:           clientIP(r),
	})
	if err != nil {
		response.Error(w, r, applyError(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.Success(w, r, http.StatusCreated, ApplicationReceipt{Received: rec.Received})
}

// UploadRequest asks for a URL to upload one file to.
type UploadRequest struct {
	// QuestionID is the file question the upload answers.
	QuestionID string `json:"question_id"`
	Filename   string `json:"filename"`
	// ContentType must be one the question accepts, e.g. application/pdf for a CV.
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
}

// UploadTarget is where and how to upload. Send the file with PUT and exactly these headers, then give token as
// the answer to the question when applying.
type UploadTarget struct {
	Token     string            `json:"token"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
	MaxBytes  int64             `json:"max_bytes"`
}

// UploadTargetEnvelope is a successful upload URL response.
type UploadTargetEnvelope struct {
	Data UploadTarget `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// presign godoc
//
//	@Summary		Get an upload URL
//	@Description	Returns a one-time URL to PUT a file to (a CV, for example). Only the file types and size the question allows are accepted: a CV is PDF only, 5 MB on the free plan and 10 MB on paid plans. Send the file with PUT and exactly the returned headers within the time shown, then give the token as the answer to the question when applying. The file is scanned for malware when the application is submitted; a file that fails is discarded. No sign-in. Rate limited.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Param			public_id	path		string					true	"Public job id"
//	@Param			body		body		hiringhttp.UploadRequest	true	"File to upload"
//	@Success		201			{object}	hiringhttp.UploadTargetEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Failure		410			{object}	apidoc.ErrorEnvelope
//	@Failure		429			{object}	apidoc.ErrorEnvelope
//	@Failure		503			{object}	apidoc.ErrorEnvelope
//	@Router			/public/jobs/{public_id}/uploads [post]
func (h *PublicHandler) presign(w http.ResponseWriter, r *http.Request) {
	if h.Uploads == nil {
		response.Error(w, r, applyError(apply.ErrFilesUnavailable))
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req UploadRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	p, err := h.Uploads.Presign(r.Context(), uploads.PresignInput{
		PublicID: chi.URLParam(r, "public_id"), QuestionID: req.QuestionID, Filename: req.Filename,
		ContentType: req.ContentType, SizeBytes: req.SizeBytes, ClientIP: clientIP(r),
	})
	if err != nil {
		response.Error(w, r, applyError(err))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.Success(w, r, http.StatusCreated, UploadTarget{
		Token: p.Token, Method: p.Method, URL: p.URL, Headers: p.Headers, ExpiresAt: p.ExpiresAt, MaxBytes: p.MaxBytes,
	})
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

// clientIP is the caller's address as the RealIP middleware left it in RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
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

// applyError maps an application error to an API error.
func applyError(err error) error {
	var ve *apply.ValidationError
	switch {
	case errors.As(err, &ve):
		return apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
			map[string]any{"fields": ve.Fields})
	case errors.Is(err, apply.ErrNotFound):
		return apperror.New(apperror.CodeNotFound, "job not found")
	case errors.Is(err, apply.ErrGone):
		return apperror.New(apperror.CodeGone, "this job is no longer available")
	case errors.Is(err, apply.ErrNotAccepting):
		return apperror.New(apperror.CodeConflict, "this job is not accepting applications")
	case errors.Is(err, apply.ErrRateLimited):
		return apperror.New(apperror.CodeTooManyAttempts, "too many applications, try again later")
	case errors.Is(err, apply.ErrFilesUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable, "file uploads are not available right now, try again later")
	case errors.Is(err, apply.ErrSensitiveUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable,
			"this application asks for sensitive information, which cannot be accepted yet")
	}
	return err
}
