package hiringhttp

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// RegisterProtectedRoutes mounts the authenticated hiring routes.
func (h *HiringHandler) RegisterProtectedRoutes(r chi.Router) {
	h.registerPublishRoutes(r)
	r.Route("/jobs", func(r chi.Router) {
		r.Get("/", h.listJobs)
		r.Post("/", h.createJob)
		h.registerPublishJobRoutes(r)
		r.Route("/{id}", func(r chi.Router) {
			h.registerPublishItemRoutes(r)
			r.Get("/", h.getJob)
			r.Patch("/", h.patchJob)
			r.Delete("/", h.deleteJob)
			r.Put("/details", h.putDetails)
			r.Put("/markets", h.putMarkets)
			r.Get("/application-form", h.getForm)
			r.Put("/application-form", h.putForm)
			r.Get("/preview", h.preview)
			r.Post("/duplicates/dismiss", h.dismissDuplicate)
		})
	})
	r.Route("/hiring", func(r chi.Router) {
		r.Get("/catalog", h.catalog)
		r.Get("/timezones", h.timezones)
		r.Get("/skills", h.skills)
		r.Get("/departments", h.departments)
		r.Post("/departments", h.addDepartment)
		r.Get("/templates", h.templates)
		r.Post("/templates", h.saveTemplate)
		r.Delete("/templates/{id}", h.deleteTemplate)
		r.Get("/settings", h.settings)
		r.Put("/settings", h.putSettings)
	})
}

// JobEnvelope is a successful single-job response.
type JobEnvelope struct {
	Data draft.JobView `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

// JobListEnvelope is a page of the jobs list.
type JobListEnvelope struct {
	Data []hiringtypes.ListItem `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

// FormEnvelope is a successful application form response.
type FormEnvelope struct {
	Data draft.FormView `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// PreviewEnvelope is the candidate-facing view of a draft.
type PreviewEnvelope struct {
	Data jobs.PublicJob `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

func (h *HiringHandler) jobID(r *http.Request) string { return chi.URLParam(r, "id") }

// listJobs godoc
//
//	@Summary		List jobs
//	@Description	Newest first. People who can only see assigned jobs get the jobs they created or were added to.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			status			query		string	false	"Comma-separated statuses, e.g. draft,published"
//	@Param			department_id	query		string	false	"Only this department"
//	@Param			q				query		string	false	"Search the title"
//	@Param			cursor			query		string	false	"next_cursor from the previous page"
//	@Param			limit			query		int		false	"Page size (default 25, max 100)"
//	@Success		200				{object}	hiringhttp.JobListEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Failure		403				{object}	apidoc.ErrorEnvelope
//	@Router			/jobs [get]
func (h *HiringHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	qv := r.URL.Query()
	q := draft.ListQuery{
		DepartmentID: qv.Get("department_id"), Query: strings.TrimSpace(qv.Get("q")), Cursor: qv.Get("cursor"),
	}
	for _, s := range strings.Split(qv.Get("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			q.Statuses = append(q.Statuses, s)
		}
	}
	if raw := qv.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		q.Limit = n
	}
	items, next, err := svc.List(r.Context(), c, q)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	more := next != ""
	response.SuccessWithMeta(w, r, http.StatusOK, nonNilItems(items), &response.Meta{NextCursor: next, HasMore: &more})
}

func nonNilItems(in []hiringtypes.ListItem) []hiringtypes.ListItem {
	if in == nil {
		return []hiringtypes.ListItem{}
	}
	return in
}

// createJob godoc
//
//	@Summary		Create a draft job
//	@Description	All fields are optional, so the page can create a draft the first time the user saves. The draft starts from the caller's saved default setup and the standard application form unless template_id or form_template_id say otherwise.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		draft.CreateRequest	false	"Job fields"
//	@Success		201		{object}	hiringhttp.JobEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs [post]
func (h *HiringHandler) createJob(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req draft.CreateRequest
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := decodeStrict(raw, &req); err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
			return
		}
	}
	v, err := svc.Create(r.Context(), c, req)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.Revision)
	w.Header().Set("Location", "/api/v1/jobs/"+v.ID)
	response.Success(w, r, http.StatusCreated, v)
}

// getJob godoc
//
//	@Summary		Get a job
//	@Description	Returns the job with warnings (market rules, pay advice, possible duplicates). The ETag header carries the revision to send back in If-Match.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.JobEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id} [get]
func (h *HiringHandler) getJob(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	v, err := svc.Get(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.Revision)
	response.Success(w, r, http.StatusOK, v)
}

func (h *HiringHandler) save(w http.ResponseWriter, r *http.Request, strict bool) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	p, err := jobs.DecodePatch(raw)
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	v, err := svc.Save(r.Context(), c, h.jobID(r), rev, p, strict)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.Revision)
	response.Success(w, r, http.StatusOK, v)
}

// patchJob godoc
//
//	@Summary		Save a draft
//	@Description	"Save as draft": send only the fields that changed. Formats are checked, required fields are not. Pay needs job.salary_range.set and retention needs job.retention.set.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string		true	"Job id"
//	@Param			If-Match	header		string		false	"Revision the client loaded"
//	@Param			body		body		jobs.Patch	true	"Fields to change"
//	@Success		200			{object}	hiringhttp.JobEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id} [patch]
func (h *HiringHandler) patchJob(w http.ResponseWriter, r *http.Request) { h.save(w, r, false) }

// putDetails godoc
//
//	@Summary		Save and continue on the job details step
//	@Description	Same body as PATCH, but the required fields (title, department, hiring option, travel, visa, employment type and, for people who can set pay, pay) must be present once applied. Moves the draft to the application form step.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string		true	"Job id"
//	@Param			If-Match	header		string		false	"Revision the client loaded"
//	@Param			body		body		jobs.Patch	true	"Fields to change"
//	@Success		200			{object}	hiringhttp.JobEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/details [put]
func (h *HiringHandler) putDetails(w http.ResponseWriter, r *http.Request) { h.save(w, r, true) }

// MarketsRequest sets where a job is open to.
type MarketsRequest struct {
	LocationMode string           `json:"location_mode"`
	Markets      []jobs.MarketSel `json:"markets"`
}

// putMarkets godoc
//
//	@Summary		Set the hiring option and markets
//	@Description	Replaces the places a job is open to. Closed markets are rejected, an anywhere job takes no places, and the retention period is raised to the strictest minimum of the chosen markets. The response carries the market warnings.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string					true	"Job id"
//	@Param			If-Match	header		string					false	"Revision the client loaded"
//	@Param			body		body		hiringhttp.MarketsRequest	true	"Markets"
//	@Success		200			{object}	hiringhttp.JobEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/markets [put]
func (h *HiringHandler) putMarkets(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req MarketsRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	markets := req.Markets
	if markets == nil {
		markets = []jobs.MarketSel{}
	}
	p := jobs.Patch{Markets: &markets}
	if req.LocationMode != "" {
		p.LocationMode = &req.LocationMode
	}
	v, err := svc.Save(r.Context(), c, h.jobID(r), rev, p, false)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.Revision)
	response.Success(w, r, http.StatusOK, v)
}

// deleteJob godoc
//
//	@Summary		Delete a draft
//	@Description	Only drafts can be deleted. Everything else is closed or archived instead.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string	true	"Job id"
//	@Param			If-Match	header		string	false	"Revision the client loaded"
//	@Success		200			{object}	map[string]bool
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id} [delete]
func (h *HiringHandler) deleteJob(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if err := svc.Delete(r.Context(), c, h.jobID(r), rev); err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, map[string]bool{"deleted": true})
}

// getForm godoc
//
//	@Summary		Get the application form
//	@Tags			application-form
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.FormEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/application-form [get]
func (h *HiringHandler) getForm(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	v, err := svc.GetForm(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.JobRevision)
	response.Success(w, r, http.StatusOK, v)
}

// putForm godoc
//
//	@Summary		Save the application form
//	@Description	Replaces the whole form. Full name and email are locked, the CV accepts PDF only, questions about current or past pay are refused, knockout rules need automated screening enabled for the company, and special category questions need a declaration and a lawful basis on the job. A knockout flags a candidate for a person to review; it never rejects anyone.
//	@Tags			application-form
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string				true	"Job id"
//	@Param			If-Match	header		string				false	"Revision the client loaded"
//	@Param			body		body		draft.FormInput		true	"Form"
//	@Success		200			{object}	hiringhttp.FormEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/application-form [put]
func (h *HiringHandler) putForm(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var in draft.FormInput
	if err := decodeStrict(raw, &in); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	v, err := svc.SaveForm(r.Context(), c, h.jobID(r), rev, in)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.JobRevision)
	response.Success(w, r, http.StatusOK, v)
}

// preview godoc
//
//	@Summary		Preview the posting
//	@Description	The candidate-facing view, built the same way as the live page. It never contains knockout rules, the lawful basis, retention or the assessment link.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.PreviewEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/preview [get]
func (h *HiringHandler) preview(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	v, err := svc.Preview(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, v)
}

// dismissDuplicate godoc
//
//	@Summary		Acknowledge the duplicate-job warning
//	@Description	Records in the audit log that the user saw the warning and kept going. The warning still shows if the job is opened again.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	map[string]bool
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/duplicates/dismiss [post]
func (h *HiringHandler) dismissDuplicate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	if err := svc.AcknowledgeDuplicate(r.Context(), c, h.jobID(r)); err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, map[string]bool{"acknowledged": true})
}

// decodeStrict parses JSON and rejects unknown fields.
func decodeStrict(raw []byte, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
