package apphttp

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Envelopes for the API documentation.
type (
	// TemplatesEnvelope is the company's email templates.
	TemplatesEnvelope struct {
		Data []applications.Template `json:"data"`
	}
	// TemplateEnvelope is one email template.
	TemplateEnvelope struct {
		Data applications.Template `json:"data"`
	}
	// MessagesEnvelope is the emails sent to a candidate.
	MessagesEnvelope struct {
		Data []applications.Message `json:"data"`
	}
	// MessageEnvelope is one email to a candidate.
	MessageEnvelope struct {
		Data applications.Message `json:"data"`
	}
	// PreviewEnvelope is an email as the candidate would get it.
	PreviewEnvelope struct {
		Data applications.Preview `json:"data"`
	}
	// EmailSettingsEnvelope is the company's email settings.
	EmailSettingsEnvelope struct {
		Data EmailSettings `json:"data"`
	}
)

// MessageRequest is an email to a candidate: a template, or a subject and body written for this message. A subject or
// body that is sent overrides the template's. Placeholders: {{candidate_name}}, {{job_title}}, {{company}} and
// {{sender_name}}.
type MessageRequest struct {
	TemplateID string `json:"template_id,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Body       string `json:"body,omitempty"`
	// ScheduleAt sends the message then (RFC 3339), between a minute and 90 days from now. Not with a decision.
	ScheduleAt *time.Time `json:"schedule_at,omitempty"`
}

func (m *MessageRequest) input() *applications.MessageInput {
	if m == nil {
		return nil
	}
	return &applications.MessageInput{TemplateID: m.TemplateID, Subject: m.Subject, Body: m.Body, ScheduleAt: m.ScheduleAt}
}

// EmailSettings are the company's candidate email settings.
type EmailSettings struct {
	// ReplyTo is where candidates' replies go. Empty means replies are not read.
	ReplyTo string `json:"reply_to"`
}

func (h *Handler) registerEmailJobRoutes(r chi.Router) {
	r.Get("/applications/{app_id}/emails", h.listMessages)
	r.Post("/applications/{app_id}/emails", h.sendMessage)
	r.Post("/applications/{app_id}/emails/preview", h.previewMessage)
	r.Delete("/applications/{app_id}/emails/{message_id}", h.cancelMessage)
}

func (h *Handler) registerEmailCompanyRoutes(r chi.Router) {
	r.Get("/email-templates", h.listTemplates)
	r.Post("/email-templates", h.createTemplate)
	r.Put("/email-templates/{template_id}", h.updateTemplate)
	r.Delete("/email-templates/{template_id}", h.deleteTemplate)
	r.Get("/email-settings", h.getEmailSettings)
	r.Put("/email-settings", h.putEmailSettings)
}

// listMessages godoc
//
//	@Summary		Emails sent to an application's candidate
//	@Description	Newest first, with scheduled ones that have not gone out. Seen by people who can see the job.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	apphttp.MessagesEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/emails [get]
func (h *Handler) listMessages(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Messages(r.Context(), c, jobID(r), appID(r))
	finish(w, r, http.StatusOK, out, err)
}

// sendMessage godoc
//
//	@Summary		Email the candidate
//	@Description	Sends now, or at `schedule_at`. The message always goes to the application's candidate: there is no recipient field. Placeholders are filled in before it is saved, so the history shows what the candidate got. At most 10 messages to one candidate and 500 by one company in 24 hours. Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any. Not for candidates who withdrew.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			app_id	path		string						true	"Application id"
//	@Param			body	body		apphttp.MessageRequest		true	"Message"
//	@Success		201		{object}	apphttp.MessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/emails [post]
func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req MessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.SendMessage(r.Context(), c, jobID(r), appID(r), *req.input())
	finish(w, r, http.StatusCreated, out, err)
}

// previewMessage godoc
//
//	@Summary		Preview an email to the candidate
//	@Description	Fills the placeholders for this candidate and returns the subject and body, without sending anything.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			app_id	path		string						true	"Application id"
//	@Param			body	body		apphttp.MessageRequest		true	"Message"
//	@Success		200		{object}	apphttp.PreviewEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/emails/preview [post]
func (h *Handler) previewMessage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req MessageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.PreviewMessage(r.Context(), c, jobID(r), appID(r), *req.input())
	finish(w, r, http.StatusOK, out, err)
}

// cancelMessage godoc
//
//	@Summary		Cancel a scheduled email
//	@Description	Only a message that has not gone out yet.
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id			path	string	true	"Job id"
//	@Param			app_id		path	string	true	"Application id"
//	@Param			message_id	path	string	true	"Message id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/emails/{message_id} [delete]
func (h *Handler) cancelMessage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.CancelMessage(r.Context(), c, jobID(r), appID(r), chi.URLParam(r, "message_id")); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listTemplates godoc
//
//	@Summary		The company's email templates
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	apphttp.TemplatesEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-templates [get]
func (h *Handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Templates(r.Context(), c)
	finish(w, r, http.StatusOK, out, err)
}

// TemplateRequest is a template's content.
type TemplateRequest struct {
	Name    string `json:"name"`
	Subject string `json:"subject"`
	// Body is plain text. Blank lines start a new paragraph. Placeholders: {{candidate_name}}, {{job_title}},
	// {{company}}, {{sender_name}}.
	Body string `json:"body"`
}

// createTemplate godoc
//
//	@Summary		Create an email template
//	@Description	Needs job.manage_any. Names are unique, ignoring case; at most 50 templates. Unknown placeholders are refused.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		apphttp.TemplateRequest	true	"Template"
//	@Success		201		{object}	apphttp.TemplateEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-templates [post]
func (h *Handler) createTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req TemplateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CreateTemplate(r.Context(), c, req.Name, req.Subject, req.Body)
	finish(w, r, http.StatusCreated, out, err)
}

// updateTemplate godoc
//
//	@Summary		Replace an email template
//	@Description	Needs job.manage_any. Messages already sent keep their text.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			template_id	path		string					true	"Template id"
//	@Param			body		body		apphttp.TemplateRequest	true	"Template"
//	@Success		200			{object}	apphttp.TemplateEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-templates/{template_id} [put]
func (h *Handler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req TemplateRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.UpdateTemplate(r.Context(), c, chi.URLParam(r, "template_id"), req.Name, req.Subject, req.Body)
	finish(w, r, http.StatusOK, out, err)
}

// deleteTemplate godoc
//
//	@Summary		Delete an email template
//	@Description	Needs job.manage_any.
//	@Tags			hiring
//	@Security		SessionCookie
//	@Param			template_id	path	string	true	"Template id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-templates/{template_id} [delete]
func (h *Handler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteTemplate(r.Context(), c, chi.URLParam(r, "template_id")); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getEmailSettings godoc
//
//	@Summary		Candidate email settings
//	@Description	The reply-to address of emails sent to candidates.
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	apphttp.EmailSettingsEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-settings [get]
func (h *Handler) getEmailSettings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	addr, err := svc.ReplyTo(r.Context(), c)
	finish(w, r, http.StatusOK, EmailSettings{ReplyTo: addr}, err)
}

// putEmailSettings godoc
//
//	@Summary		Change candidate email settings
//	@Description	Needs job.manage_any. An empty `reply_to` clears it: candidates are then told replies are not read. OpenHR does not read replies; they go to this address.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		apphttp.EmailSettings	true	"Settings"
//	@Success		200		{object}	apphttp.EmailSettingsEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/email-settings [put]
func (h *Handler) putEmailSettings(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req EmailSettings
	if !decodeBody(w, r, &req) {
		return
	}
	addr, err := svc.SetReplyTo(r.Context(), c, req.ReplyTo)
	finish(w, r, http.StatusOK, EmailSettings{ReplyTo: addr}, err)
}
