package hiringhttp

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// CatalogEnvelope is the pick lists of the job form.
type CatalogEnvelope struct {
	Data draft.Catalog `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

// TimezonesEnvelope lists the time zones a job can be open to.
type TimezonesEnvelope struct {
	Data []jobs.Timezone `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// SkillsEnvelope lists skills from the catalog.
type SkillsEnvelope struct {
	Data []hiringtypes.CatalogItem `json:"data"`
	Meta *apidoc.Meta              `json:"meta,omitempty"`
}

// MeEnvelope is the caller's roles and permissions.
type MeEnvelope struct {
	Data draft.MeView `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// PeopleEnvelope lists the company's members.
type PeopleEnvelope struct {
	Data []hiringtypes.Person `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

// DepartmentsEnvelope lists departments.
type DepartmentsEnvelope struct {
	Data []hiringtypes.Department `json:"data"`
	Meta *apidoc.Meta             `json:"meta,omitempty"`
}

// DepartmentEnvelope is one department.
type DepartmentEnvelope struct {
	Data hiringtypes.Department `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

// TemplatesEnvelope lists templates.
type TemplatesEnvelope struct {
	Data []hiringtypes.Template `json:"data"`
	Meta *apidoc.Meta           `json:"meta,omitempty"`
}

// TemplateEnvelope is one template.
type TemplateEnvelope struct {
	Data hiringtypes.Template `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

// SettingsEnvelope is the company's hiring settings.
type SettingsEnvelope struct {
	Data hiringtypes.Settings `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

// DepartmentRequest names a department.
type DepartmentRequest struct {
	Name string `json:"name" example:"Customer Support"`
}

// SettingsRequest changes the company's hiring settings.
type SettingsRequest struct {
	AutomatedScreeningEnabled bool `json:"automated_screening_enabled"`
}

// catalog godoc
//
//	@Summary		Job form pick lists
//	@Description	Fixed lists (employment type, pay, travel, visa, lawful basis, currencies, retention options), seniority, experience, industries and every market with its warnings and whether it is open.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.CatalogEnvelope
//	@Router			/hiring/catalog [get]
func (h *HiringHandler) catalog(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	cat, err := svc.Catalog(r.Context())
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, cat)
}

// timezones godoc
//
//	@Summary		Time zones
//	@Description	Time zones with their current UTC offset, for jobs that are open to a time zone rather than a place.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.TimezonesEnvelope
//	@Router			/hiring/timezones [get]
func (h *HiringHandler) timezones(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.Identify(r.Context()); !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	response.Success(w, r, http.StatusOK, jobs.Timezones(time.Now()))
}

// skills godoc
//
//	@Summary		Search skills
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Param			q		query		string	false	"Start of the skill name"
//	@Param			limit	query		int		false	"Most results (default 20, max 50)"
//	@Success		200		{object}	hiringhttp.SkillsEnvelope
//	@Router			/hiring/skills [get]
func (h *HiringHandler) skills(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		limit = n
	}
	items, err := svc.Skills(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, items)
}

// me godoc
//
//	@Summary		My hiring roles and permissions
//	@Description	The signed-in person's company roles and the permissions they add up to, such as job.export. Use it to show or hide actions; the server still checks every request.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.MeEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/me [get]
func (h *HiringHandler) me(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	response.Success(w, r, http.StatusOK, svc.Me(c))
}

// people godoc
//
//	@Summary		List the company's people
//	@Description	Members of the caller's company, with their roles, for the "add people to this job" picker. Search matches the name or email. Returns at most 50 people. Needs job.collaborator.add or job.collaborator.add_limited.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Param			q	query		string	false	"Part of a name or email"
//	@Success		200	{object}	hiringhttp.PeopleEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/people [get]
func (h *HiringHandler) people(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	list, err := svc.People(r.Context(), c, strings.TrimSpace(r.URL.Query().Get("q")))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, list)
}

// departments godoc
//
//	@Summary		List departments
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.DepartmentsEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/departments [get]
func (h *HiringHandler) departments(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	list, err := svc.Departments(r.Context(), c)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, list)
}

// addDepartment godoc
//
//	@Summary		Add a department
//	@Description	Names are unique per company ignoring case; adding an existing name returns the existing department.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		hiringhttp.DepartmentRequest	true	"Department"
//	@Success		200		{object}	hiringhttp.DepartmentEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/departments [post]
func (h *HiringHandler) addDepartment(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req DepartmentRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	d, err := svc.AddDepartment(r.Context(), c, req.Name)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, d)
}

// templates godoc
//
//	@Summary		List templates
//	@Description	Company templates plus the caller's own default setup.
//	@Tags			templates
//	@Produce		json
//	@Security		BearerAuth
//	@Param			kind	query		string	false	"job_details or application_form"	Enums(job_details, application_form)
//	@Success		200		{object}	hiringhttp.TemplatesEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/templates [get]
func (h *HiringHandler) templates(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	list, err := svc.Templates(r.Context(), c, strings.TrimSpace(r.URL.Query().Get("kind")))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, list)
}

// saveTemplate godoc
//
//	@Summary		Save a job's setup as a template
//	@Description	The template is made from an existing job, so it always matches what the builders produce. Set as_my_default to keep the setup for the caller's future jobs ("Keep this setup"); otherwise name it to share it with the company. Titles and closing dates are never saved, and pay and retention only when the caller may set them.
//	@Tags			templates
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		draft.TemplateInput	true	"Template"
//	@Success		201		{object}	hiringhttp.TemplateEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/templates [post]
func (h *HiringHandler) saveTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var in draft.TemplateInput
	if err := decodeStrict(raw, &in); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	t, err := svc.SaveTemplate(r.Context(), c, in)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusCreated, t)
}

// deleteTemplate godoc
//
//	@Summary		Delete a template
//	@Tags			templates
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Template id"
//	@Success		200	{object}	map[string]bool
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/templates/{id} [delete]
func (h *HiringHandler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteTemplate(r.Context(), c, h.jobID(r)); err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, map[string]bool{"deleted": true})
}

// settings godoc
//
//	@Summary		Hiring settings
//	@Description	Company-level switches. Automated screening (knockout questions) is off until the company's legal contact turns it on.
//	@Tags			hiring
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	hiringhttp.SettingsEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/settings [get]
func (h *HiringHandler) settings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	st, err := svc.Settings(r.Context(), c)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, st)
}

// putSettings godoc
//
//	@Summary		Change hiring settings
//	@Description	Needs form.automated_screening.enable (Legal).
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		hiringhttp.SettingsRequest	true	"Settings"
//	@Success		200		{object}	hiringhttp.SettingsEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/settings [put]
func (h *HiringHandler) putSettings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	var req SettingsRequest
	if err := decodeStrict(raw, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	st, err := svc.SetAutomatedScreening(r.Context(), c, req.AutomatedScreeningEnabled)
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	response.Success(w, r, http.StatusOK, st)
}
