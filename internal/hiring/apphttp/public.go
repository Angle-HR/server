package apphttp

import (
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/apply"
	"github.com/Angle-HR/server/internal/hiring/uploads"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// RegisterPublicRoutes mounts the routes candidates reach without signing in. r is the router of /public/jobs.
func (h *Handler) RegisterPublicRoutes(r chi.Router) {
	r.Post("/{public_id}/uploads", h.presign)
	r.Post("/{public_id}/applications", h.submit)
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

// submit godoc
//
//	@Summary		Apply for a job
//	@Description	Sends an application. No sign-in. The answers are checked against the form that was frozen when the job was published. A failed knockout or screening rule flags the application for a person to review; it never rejects it. Applying again with the same email returns the same success and changes nothing. Rate limited.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Param			public_id			path		string							true	"Public job id"
//	@Param			Idempotency-Key		header		string							false	"A random 8 to 64 character key; a retry with the same key never creates a second application"
//	@Param			body				body		apphttp.ApplicationRequest	true	"Application"
//	@Success		201					{object}	apphttp.ApplicationReceiptEnvelope
//	@Failure		400					{object}	apidoc.ErrorEnvelope
//	@Failure		404					{object}	apidoc.ErrorEnvelope
//	@Failure		409					{object}	apidoc.ErrorEnvelope
//	@Failure		410					{object}	apidoc.ErrorEnvelope
//	@Failure		429					{object}	apidoc.ErrorEnvelope
//	@Failure		503					{object}	apidoc.ErrorEnvelope
//	@Router			/public/jobs/{public_id}/applications [post]
func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	if h.Apply == nil {
		response.Error(w, r, apperror.New(apperror.CodeServiceUnavailable, "applications are not available"))
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req ApplicationRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
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
		writeApplyError(w, r, err)
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
//	@Param			body		body		apphttp.UploadRequest	true	"File to upload"
//	@Success		201			{object}	apphttp.UploadTargetEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Failure		410			{object}	apidoc.ErrorEnvelope
//	@Failure		429			{object}	apidoc.ErrorEnvelope
//	@Failure		503			{object}	apidoc.ErrorEnvelope
//	@Router			/public/jobs/{public_id}/uploads [post]
func (h *Handler) presign(w http.ResponseWriter, r *http.Request) {
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
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	p, err := h.Uploads.Presign(r.Context(), uploads.PresignInput{
		PublicID: chi.URLParam(r, "public_id"), QuestionID: req.QuestionID, Filename: req.Filename,
		ContentType: req.ContentType, SizeBytes: req.SizeBytes, ClientIP: clientIP(r),
	})
	if err != nil {
		writeApplyError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response.Success(w, r, http.StatusCreated, UploadTarget{
		Token: p.Token, Method: p.Method, URL: p.URL, Headers: p.Headers, ExpiresAt: p.ExpiresAt, MaxBytes: p.MaxBytes,
	})
}

// clientIP is the caller's address as the RealIP middleware left it in RemoteAddr.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

// writeApplyError writes applyError's result. A rate limit tells the caller how long to wait, in whole seconds.
func writeApplyError(w http.ResponseWriter, r *http.Request, err error) {
	var rl *apply.RateLimitedError
	if errors.As(err, &rl) {
		secs := int(math.Ceil(rl.RetryAfter.Seconds()))
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
	}
	response.Error(w, r, applyError(err))
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
	case errors.Is(err, apply.ErrIdempotencyKeyReused):
		return apperror.New(apperror.CodeIdempotencyKeyReused,
			"this Idempotency-Key was already used for a different application; send a new key")
	case errors.Is(err, apply.ErrRateLimited):
		return apperror.New(apperror.CodeTooManyRequests, "too many applications, try again later")
	case errors.Is(err, apply.ErrFilesUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable, "file uploads are not available right now, try again later")
	case errors.Is(err, apply.ErrSensitiveUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable,
			"this application asks for sensitive information, which cannot be accepted yet")
	}
	return err
}
