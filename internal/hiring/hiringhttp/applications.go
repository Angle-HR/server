package hiringhttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// RegionalApplications returns the applications service of a region.
type RegionalApplications func(reg region.Region) (*applications.Service, error)

// Envelopes for the API documentation.
type (
	// ApplicationPageEnvelope is one page of a job's applications.
	ApplicationPageEnvelope struct {
		Data applications.Page `json:"data"`
	}
	// ApplicationEnvelope is one application.
	ApplicationEnvelope struct {
		Data applications.Detail `json:"data"`
	}
	// ApplicationResultEnvelope is the outcome of a status change or review.
	ApplicationResultEnvelope struct {
		Data applications.Result `json:"data"`
	}
	// FileLinkEnvelope is a short-lived download link.
	FileLinkEnvelope struct {
		Data applications.FileLink `json:"data"`
	}
	// ApplicantSettingsEnvelope is the company's candidate settings.
	ApplicantSettingsEnvelope struct {
		Data applications.ApplicantSettings `json:"data"`
	}
)

// registerApplicationRoutes mounts the per-job routes inside /jobs/{id}. Nothing is mounted when the handler has
// no applications service.
func (h *HiringHandler) registerApplicationRoutes(r chi.Router) {
	r.Get("/applications", h.listApplications)
	r.Get("/applications/{app_id}", h.getApplication)
	r.Post("/applications/{app_id}/status", h.setApplicationStatus)
	r.Post("/applications/{app_id}/review", h.reviewApplication)
	r.Get("/applications/{app_id}/files/{file_id}", h.applicationFile)
}

// registerApplicantSettingsRoutes mounts the company-level candidate settings inside /hiring.
func (h *HiringHandler) registerApplicantSettingsRoutes(r chi.Router) {
	r.Get("/applicant-settings", h.getApplicantSettings)
	r.Put("/applicant-settings", h.putApplicantSettings)
}

// appScope resolves who is calling and the applications service of their region.
func (h *HiringHandler) appScope(w http.ResponseWriter, r *http.Request) (*applications.Service, draft.Caller, bool) {
	if h.Applications == nil {
		response.Error(w, r, apperror.New(apperror.CodeServiceUnavailable, "applications are not available"))
		return nil, draft.Caller{}, false
	}
	userID, reg, ok := h.Identify(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return nil, draft.Caller{}, false
	}
	_, c, err := h.Directory.Scope(r.Context(), userID, reg)
	if err != nil {
		if errors.Is(err, ErrNoOrganization) {
			err = apperror.New(apperror.CodeNotFound, "no organization for this account")
		}
		response.Error(w, r, err)
		return nil, draft.Caller{}, false
	}
	svc, err := h.Applications(reg)
	if err != nil {
		response.Error(w, r, err)
		return nil, draft.Caller{}, false
	}
	return svc, c, true
}

// applicationsError maps the pipeline errors, then falls back to the hiring mapping.
func applicationsError(err error) error {
	reason := ""
	msg := ""
	switch {
	case errors.Is(err, applications.ErrInvalidTransition):
		reason, msg = "invalid_transition", "the application cannot move to that status from where it is"
	case errors.Is(err, applications.ErrReviewRequired):
		reason, msg = "review_required", "a flagged application is decided through its review"
	case errors.Is(err, applications.ErrNoReview):
		reason, msg = "no_open_review", "this application has no open review"
	case errors.Is(err, applications.ErrNotOpened):
		reason, msg = "not_opened", "open the application yourself before deciding it"
	case errors.Is(err, applications.ErrStale):
		reason, msg = "changed", "the application was changed by someone else; reload it and try again"
	default:
		return hiringError(err)
	}
	return apperror.NewWithDetails(apperror.CodeConflict, msg, map[string]any{"reason": reason})
}

// noStore keeps candidate data out of shared caches.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
}

func (h *HiringHandler) appID(r *http.Request) string  { return chi.URLParam(r, "app_id") }
func (h *HiringHandler) fileID(r *http.Request) string { return chi.URLParam(r, "file_id") }

// listApplications godoc
//
//	@Summary		List a job's applications
//	@Description	Newest first, 25 per page by default (100 at most). Rows carry no answers. `status` can be repeated or comma separated; `new=true` lists applications nobody has opened. `counts` are for the whole job, whatever the filters. Seen by people who can see the job.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Job id"
//	@Param			status	query		[]string	false	"received, in_review, flagged, rejected, hired or withdrawn"
//	@Param			source	query		string	false	"Where the candidate came from"
//	@Param			q		query		string	false	"Search name or email"
//	@Param			new		query		bool	false	"Only applications nobody has opened"
//	@Param			cursor	query		string	false	"next_cursor of the previous page"
//	@Param			limit	query		int		false	"Page size"
//	@Success		200		{object}	hiringhttp.ApplicationPageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications [get]
func (h *HiringHandler) listApplications(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	qs := r.URL.Query()
	q := applications.ListQuery{
		Source: qs.Get("source"), Query: qs.Get("q"), Cursor: qs.Get("cursor"), NewOnly: qs.Get("new") == "true",
	}
	for _, v := range qs["status"] {
		for _, st := range strings.Split(v, ",") {
			if st = strings.TrimSpace(st); st != "" {
				q.Statuses = append(q.Statuses, st)
			}
		}
	}
	if raw := qs.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	page, err := svc.List(r.Context(), c, h.jobID(r), q)
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	noStore(w)
	response.Success(w, r, http.StatusOK, page)
}

// getApplication godoc
//
//	@Summary		Open an application
//	@Description	Returns the answers (special category answers are never returned), the files, the flags and the review. Opening is recorded in the job's audit log; the first opening takes the application out of the job's new count. Whoever may decide a flagged application and opens it becomes its reviewer.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	hiringhttp.ApplicationEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id} [get]
func (h *HiringHandler) getApplication(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	d, err := svc.Get(r.Context(), c, h.jobID(r), h.appID(r))
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	noStore(w)
	response.Success(w, r, http.StatusOK, d)
}

// ApplicationStatusRequest is a plain status change.
type ApplicationStatusRequest struct {
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// setApplicationStatus godoc
//
//	@Summary		Change an application's status
//	@Description	received to in_review or withdrawn; in_review to rejected, hired or withdrawn; flagged to withdrawn only. A flagged application is decided through its review. rejected, hired and withdrawn are final and start the retention clock. A rejection or hire emails the candidate unless the company turned that off. Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Job id"
//	@Param			app_id	path		string								true	"Application id"
//	@Param			body	body		hiringhttp.ApplicationStatusRequest	true	"New status"
//	@Success		200		{object}	hiringhttp.ApplicationResultEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/status [post]
func (h *HiringHandler) setApplicationStatus(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req ApplicationStatusRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	res, err := svc.SetStatus(r.Context(), c, h.jobID(r), h.appID(r), applications.StatusInput{Status: req.Status, Note: req.Note})
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	response.Success(w, r, http.StatusOK, res)
}

// ApplicationReviewRequest is the decision on a flagged application.
type ApplicationReviewRequest struct {
	Decision string `json:"decision"`
	Note     string `json:"note,omitempty"`
}

// reviewApplication godoc
//
//	@Summary		Decide a flagged application
//	@Description	`proceed` moves it to in_review; `reject` rejects it. The person deciding must be the last to have opened the application (GET it first), so every flagged candidate is looked at by a person before they are decided. There is no bulk version.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Job id"
//	@Param			app_id	path		string								true	"Application id"
//	@Param			body	body		hiringhttp.ApplicationReviewRequest	true	"Decision"
//	@Success		200		{object}	hiringhttp.ApplicationResultEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/review [post]
func (h *HiringHandler) reviewApplication(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req ApplicationReviewRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	res, err := svc.Review(r.Context(), c, h.jobID(r), h.appID(r), applications.ReviewInput{Decision: req.Decision, Note: req.Note})
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	response.Success(w, r, http.StatusOK, res)
}

// applicationFile godoc
//
//	@Summary		Get a download link for an application's file
//	@Description	Returns a link that works for 5 minutes and downloads the file under its original name. The request is recorded in the job's audit log. Ask again for a new link.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Param			file_id	path		string	true	"File id"
//	@Success		200		{object}	hiringhttp.FileLinkEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/files/{file_id} [get]
func (h *HiringHandler) applicationFile(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	l, err := svc.File(r.Context(), c, h.jobID(r), h.appID(r), h.fileID(r))
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	noStore(w)
	response.Success(w, r, http.StatusOK, l)
}

// getApplicantSettings godoc
//
//	@Summary		Candidate settings
//	@Description	Whether rejected candidates are blocked from reapplying (0 or 6 months) and whether candidates are emailed when rejected or hired.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.ApplicantSettingsEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/applicant-settings [get]
func (h *HiringHandler) getApplicantSettings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	st, err := svc.Settings(r.Context(), c)
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	response.Success(w, r, http.StatusOK, st)
}

// ApplicantSettingsRequest changes candidate settings; a field left out stays as it is.
type ApplicantSettingsRequest struct {
	ReapplyBlockMonths *int  `json:"reapply_block_months,omitempty"`
	SendStatusEmails   *bool `json:"send_status_emails,omitempty"`
}

// putApplicantSettings godoc
//
//	@Summary		Change candidate settings
//	@Description	Needs job.retention.set. Blocking reapplications (6 months) cannot be turned on while the server has no key for it.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		hiringhttp.ApplicantSettingsRequest	true	"Settings"
//	@Success		200		{object}	hiringhttp.ApplicantSettingsEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/applicant-settings [put]
func (h *HiringHandler) putApplicantSettings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req ApplicantSettingsRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	st, err := svc.SaveSettings(r.Context(), c, applications.SettingsInput{
		ReapplyBlockMonths: req.ReapplyBlockMonths, SendStatusEmails: req.SendStatusEmails,
	})
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	response.Success(w, r, http.StatusOK, st)
}
