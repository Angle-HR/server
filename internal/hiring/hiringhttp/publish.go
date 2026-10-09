package hiringhttp

import (
	"encoding/csv"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// Keep the error envelope in this file so swag can resolve the failure responses.
var _ = apidoc.ErrorEnvelope{}

// lifecycleRoutes maps a path segment to the lifecycle action it performs. Publishing has its own handler
// because the caller decides between publishing and submitting for approval.
var lifecycleRoutes = map[string]string{
	"pause": "pause", "resume": "resume", "close": "close", "reopen": "reopen",
	"archive": "archive", "to-draft": "to_draft", "withdraw": "withdraw",
}

// registerPublishJobRoutes mounts the collection-level routes inside /jobs.
func (h *HiringHandler) registerPublishJobRoutes(r chi.Router) {
	r.Post("/bulk", h.bulk)
	r.Get("/export", h.export)
}

// registerPublishItemRoutes mounts the per-job routes inside /jobs/{id}.
func (h *HiringHandler) registerPublishItemRoutes(r chi.Router) {
	r.Get("/publish-check", h.publishCheck)
	r.Post("/publish", h.publish)
	for segment, action := range lifecycleRoutes {
		r.Post("/"+segment, h.lifecycle(action))
	}
	r.Post("/duplicate", h.duplicate)
	r.Get("/compliance", h.compliance)
	r.Put("/compliance/{gate_id}", h.confirmGate)
	r.Get("/members", h.getMembers)
	r.Put("/members", h.putMembers)
	r.Get("/disqualification-rules", h.getRules)
	r.Put("/disqualification-rules", h.putRules)
}

// registerPublishRoutes mounts the organization-level routes.
func (h *HiringHandler) registerPublishRoutes(r chi.Router) {
	r.Get("/organization/agreements", h.companySetup)
	r.Post("/organization/agreements", h.acceptAgreement)
	r.Put("/organization/privacy-contact", h.putPrivacyContact)
	r.Put("/organization/dpia", h.putDPIA)
}

// Envelopes for the API documentation.
type (
	// ReportEnvelope is the dry run of the publish checks.
	ReportEnvelope struct {
		Data gates.Report `json:"data"`
	}
	// TransitionEnvelope is a job after a status change.
	TransitionEnvelope struct {
		Data draft.TransitionResult `json:"data"`
	}
	// GatesEnvelope lists the compliance gates of a job.
	GatesEnvelope struct {
		Data []gates.State `json:"data"`
	}
	// MembersEnvelope lists a job's hiring team.
	MembersEnvelope struct {
		Data []hiringtypes.Member `json:"data"`
	}
	// RulesEnvelope is a job's screening rules.
	RulesEnvelope struct {
		Data draft.RulesView `json:"data"`
	}
	// BulkEnvelope is the outcome of a bulk action.
	BulkEnvelope struct {
		Data draft.BulkResult `json:"data"`
	}
	// CompanySetupEnvelope is the company's publish prerequisites.
	CompanySetupEnvelope struct {
		Data hiringtypes.CompanySetup `json:"data"`
	}
)

// publishCheck godoc
//
//	@Summary		Check whether a job can publish
//	@Description	The dry run of publishing: the same checks, nothing written. Blocking issues stop a publish; warnings do not. Also lists the compliance gates that apply and where each stands.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.ReportEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/publish-check [get]
func (h *HiringHandler) publishCheck(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rep, err := svc.PublishCheck(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, rep)
}

// publish godoc
//
//	@Summary		Publish a job
//	@Description	Runs every publish check inside the same transaction as the status change: a verified company, an accepted DPA, a privacy contact, complete details, an open market, a lawful basis, the screening prerequisites and every required compliance gate. People who may not publish submit the job for approval instead. A blocked publish returns 422 with every issue.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string	true	"Job id"
//	@Param			If-Match	header		string	false	"Revision the client loaded"
//	@Success		200			{object}	hiringhttp.TransitionEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Failure		422			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/publish [post]
func (h *HiringHandler) publish(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	rev, ok := ifMatch(w, r)
	if !ok {
		return
	}
	res, err := svc.Publish(r.Context(), c, h.jobID(r), rev)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, res.Job.Revision)
	response.Success(w, r, http.StatusOK, res)
}

// lifecycle serves pause, resume, close, reopen, archive, to-draft and withdraw.
//
//	@Summary		Change a job's status
//	@Description	One path per action: pause, resume, close, reopen, archive, to-draft and withdraw. Pause, close and move-to-draft take a job off every board. Resume and reopen run the publish checks again. HR 2 can change only the jobs it created. Which statuses allow which action is in the status rules of the lifecycle package.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string	true	"Job id"
//	@Param			If-Match	header		string	false	"Revision the client loaded"
//	@Success		200			{object}	hiringhttp.TransitionEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Failure		422			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/pause [post]
//	@Router			/jobs/{id}/resume [post]
//	@Router			/jobs/{id}/close [post]
//	@Router			/jobs/{id}/reopen [post]
//	@Router			/jobs/{id}/archive [post]
//	@Router			/jobs/{id}/to-draft [post]
//	@Router			/jobs/{id}/withdraw [post]
func (h *HiringHandler) lifecycle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svc, c, ok := h.scope(w, r)
		if !ok {
			return
		}
		rev, ok := ifMatch(w, r)
		if !ok {
			return
		}
		res, err := svc.Transition(r.Context(), c, h.jobID(r), action, rev)
		if err != nil {
			response.Error(w, r, hiringError(err))
			return
		}
		setETag(w, res.Job.Revision)
		response.Success(w, r, http.StatusOK, res)
	}
}

// duplicate godoc
//
//	@Summary		Duplicate a job
//	@Description	Copies the details and application form into a new draft owned by the caller. The copy has no team, no screening rules, no confirmations and no public id.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		201	{object}	hiringhttp.JobEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/duplicate [post]
func (h *HiringHandler) duplicate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	v, err := svc.Duplicate(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	setETag(w, v.Revision)
	w.Header().Set("Location", "/api/v1/jobs/"+v.ID)
	response.Success(w, r, http.StatusCreated, v)
}

// compliance godoc
//
//	@Summary		List a job's compliance gates
//	@Description	The gates that apply to the job's markets, special categories and screening, with where each stands. Gates marked auto are answered from the job's own details.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.GatesEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/compliance [get]
func (h *HiringHandler) compliance(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	states, err := svc.Compliance(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, states)
}

// ConfirmRequest confirms or withdraws one compliance gate.
type ConfirmRequest struct {
	Confirmed bool `json:"confirmed"`
}

// confirmGate godoc
//
//	@Summary		Confirm or withdraw a compliance gate
//	@Description	The confirmation is the company's own and is written to the append-only compliance log against the caller. A confirmation stops counting when the gate's wording changes.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string						true	"Job id"
//	@Param			gate_id		path		string						true	"Gate id, e.g. uk-lawful"
//	@Param			If-Match	header		string						false	"Revision the client loaded"
//	@Param			body		body		hiringhttp.ConfirmRequest	true	"Confirmation"
//	@Success		200			{object}	hiringhttp.GatesEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/compliance/{gate_id} [put]
func (h *HiringHandler) confirmGate(w http.ResponseWriter, r *http.Request) {
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
	var req ConfirmRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	states, err := svc.ConfirmGate(r.Context(), c, h.jobID(r), chi.URLParam(r, "gate_id"), req.Confirmed, rev)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, states)
}

// getMembers godoc
//
//	@Summary		List a job's hiring team
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.MembersEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/members [get]
func (h *HiringHandler) getMembers(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	ms, err := svc.Members(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, ms)
}

// MembersRequest replaces a job's hiring team.
type MembersRequest struct {
	Members []draft.MemberInput `json:"members"`
}

// putMembers godoc
//
//	@Summary		Set a job's hiring team
//	@Description	Replaces the team (the Permissions step). The creator already has access and is not listed. Team members see the job and its candidates but gain no edit rights. HR 2 can add and remove viewers on its own jobs only.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string						true	"Job id"
//	@Param			If-Match	header		string						false	"Revision the client loaded"
//	@Param			body		body		hiringhttp.MembersRequest	true	"The whole team"
//	@Success		200			{object}	hiringhttp.MembersEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/members [put]
func (h *HiringHandler) putMembers(w http.ResponseWriter, r *http.Request) {
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
	var req MembersRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	ms, err := svc.SetMembers(r.Context(), c, h.jobID(r), rev, req.Members)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, ms)
}

// getRules godoc
//
//	@Summary		Get a job's screening rules
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.RulesEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/disqualification-rules [get]
func (h *HiringHandler) getRules(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	v, err := svc.Rules(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, v)
}

// putRules godoc
//
//	@Summary		Set a draft's screening rules
//	@Description	Replaces the rules. A rule flags a candidate for a person to review; it never rejects anyone. Rules count as automated screening, so they need Legal to have enabled screening for the company and, before publishing, the company DPIA and this job's DPIA scope confirmation.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id			path		string			true	"Job id"
//	@Param			If-Match	header		string			false	"Revision the client loaded"
//	@Param			body		body		draft.RulesInput	true	"Rules"
//	@Success		200			{object}	hiringhttp.RulesEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/disqualification-rules [put]
func (h *HiringHandler) putRules(w http.ResponseWriter, r *http.Request) {
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
	var req draft.RulesInput
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	v, err := svc.SetRules(r.Context(), c, h.jobID(r), rev, req)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, v)
}

// BulkRequest selects jobs and an action.
type BulkRequest struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
}

// bulk godoc
//
//	@Summary		Change several jobs at once
//	@Description	Pause, close, archive or move to draft, for up to 100 jobs. Not all-or-nothing: each job is changed on its own, and the ones that could not be are listed with the reason. The result carries each job's earlier status so the client can offer undo.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		hiringhttp.BulkRequest	true	"Action and job ids"
//	@Success		200		{object}	hiringhttp.BulkEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/bulk [post]
func (h *HiringHandler) bulk(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req BulkRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	res, err := svc.Bulk(r.Context(), c, req.Action, req.IDs)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, res)
}

// export godoc
//
//	@Summary		Export jobs as CSV or JSON
//	@Description	Every job the caller may see, newest first, or only the jobs in ids. CSV is the default; format=json returns the same rows as on the jobs list. Needs job.export.
//	@Tags			jobs
//	@Produce		text/csv,json
//	@Security		BearerAuth
//	@Param			status	query	string	false	"Comma-separated statuses"
//	@Param			ids		query	string	false	"Comma-separated job ids; only these jobs are exported"
//	@Param			format	query	string	false	"csv (default) or json"
//	@Success		200		{string}	string	"CSV"
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/export [get]
func (h *HiringHandler) export(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	var statuses, ids []string
	for _, s := range strings.Split(r.URL.Query().Get("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			statuses = append(statuses, s)
		}
	}
	for _, s := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			ids = append(ids, s)
		}
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format != "" && format != "csv" && format != "json" {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "format must be csv or json"))
		return
	}
	rows, err := svc.ExportSelected(r.Context(), c, statuses, ids)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	if format == "json" {
		response.Success(w, r, http.StatusOK, nonNilItems(rows))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="jobs.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"job_code", "title", "status", "department", "employment_type", "markets", "created_at", "updated_at"})
	for _, it := range rows {
		_ = cw.Write([]string{
			csvSafe(it.JobCode), csvSafe(it.Title), it.Status, csvSafe(it.DepartmentName), it.EmploymentType,
			strings.Join(it.Markets, " "), it.CreatedAt, it.UpdatedAt,
		})
	}
	cw.Flush()
}

// csvSafe stops a cell from being read as a formula when the file is opened in a spreadsheet: a title that
// starts with =, +, - or @ is prefixed with an apostrophe.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// ---- company setup ----

// companySetup godoc
//
//	@Summary		Show the company's publishing requirements
//	@Description	Verification status, the DPA version to accept and whether it is accepted, the privacy contact and the DPIA.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.CompanySetupEnvelope
//	@Router			/organization/agreements [get]
func (h *HiringHandler) companySetup(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	out, err := svc.CompanySetup(r.Context(), c)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, out)
}

// AcceptRequest accepts a version of the terms and DPA.
type AcceptRequest struct {
	Version string `json:"version"`
}

// acceptAgreement godoc
//
//	@Summary		Accept the terms and data processing agreement
//	@Description	Only a Founder or Legal can. The version must be the current one shown by GET.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		hiringhttp.AcceptRequest	true	"Version"
//	@Success		200		{object}	hiringhttp.CompanySetupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/agreements [post]
func (h *HiringHandler) acceptAgreement(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req AcceptRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	out, err := svc.AcceptDPA(r.Context(), c, req.Version)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, out)
}

// putPrivacyContact godoc
//
//	@Summary		Set the privacy contact
//	@Description	The address applicants are told to write to, and an optional DPO contact. Needed before publishing.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		draft.PrivacyContactInput	true	"Contact"
//	@Success		200		{object}	hiringhttp.CompanySetupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/privacy-contact [put]
func (h *HiringHandler) putPrivacyContact(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req draft.PrivacyContactInput
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	out, err := svc.SetPrivacyContact(r.Context(), c, req)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, out)
}

// putDPIA godoc
//
//	@Summary		Record the company's DPIA
//	@Description	Legal records the data protection impact assessment before the first screening job publishes. Each call stores a new version.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		draft.DPIAInput	true	"Where the assessment is kept and what it covers"
//	@Success		200		{object}	hiringhttp.CompanySetupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/dpia [put]
func (h *HiringHandler) putDPIA(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req draft.DPIAInput
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	out, err := svc.RecordDPIA(r.Context(), c, req)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, out)
}
