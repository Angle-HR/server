package apphttp

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Envelopes for the API documentation.
type (
	// InterviewsEnvelope is a list of interviews.
	InterviewsEnvelope struct {
		Data []applications.Interview `json:"data"`
	}
	// InterviewEnvelope is one interview.
	InterviewEnvelope struct {
		Data applications.Interview `json:"data"`
	}
	// CompareEnvelope is a side-by-side view of applications.
	CompareEnvelope struct {
		Data applications.CompareResult `json:"data"`
	}
)

func (h *Handler) registerInterviewJobRoutes(r chi.Router) {
	r.Get("/applications/compare", h.compare)
	r.Get("/applications/{app_id}/interviews", h.listInterviews)
	r.Post("/applications/{app_id}/interviews", h.scheduleInterview)
	r.Patch("/applications/{app_id}/interviews/{interview_id}", h.updateInterview)
	r.Post("/applications/{app_id}/interviews/{interview_id}/cancel", h.cancelInterview)
	r.Post("/applications/{app_id}/interviews/{interview_id}/complete", h.completeInterview)
}

func (h *Handler) registerInterviewCompanyRoutes(r chi.Router) {
	r.Get("/interviews/mine", h.mySchedule)
}

func interviewID(r *http.Request) string { return chi.URLParam(r, "interview_id") }

// InterviewRequest schedules an interview.
type InterviewRequest struct {
	// StartsAt is RFC 3339, with an offset or Z.
	StartsAt time.Time `json:"starts_at"`
	// DurationMinutes is 15 to 480; 60 when left out.
	DurationMinutes int `json:"duration_minutes,omitempty"`
	// Timezone is the IANA zone the interview is planned in, for example Europe/London.
	Timezone string `json:"timezone"`
	// Kind is video, phone or onsite.
	Kind string `json:"kind"`
	// Location is the https meeting link for video, the address for onsite, and optional for phone.
	Location string `json:"location,omitempty"`
	// Notes are internal; candidates never see them.
	Notes string `json:"notes,omitempty"`
	// InterviewerIDs are people on the job's team (at most 10).
	InterviewerIDs []string `json:"interviewer_ids,omitempty"`
	// Invite emails the candidate a calendar invite. Needs the company's reply-to address (/hiring/email-settings).
	Invite     bool   `json:"invite,omitempty"`
	InviteNote string `json:"invite_note,omitempty"`
}

// InterviewPatchRequest changes an interview; a field left out stays as it is.
type InterviewPatchRequest struct {
	StartsAt        *time.Time `json:"starts_at,omitempty"`
	DurationMinutes *int       `json:"duration_minutes,omitempty"`
	Timezone        *string    `json:"timezone,omitempty"`
	Kind            *string    `json:"kind,omitempty"`
	Location        *string    `json:"location,omitempty"`
	Notes           *string    `json:"notes,omitempty"`
	InterviewerIDs  *[]string  `json:"interviewer_ids,omitempty"`
	// Invite emails the candidate the updated calendar event.
	Invite     bool   `json:"invite,omitempty"`
	InviteNote string `json:"invite_note,omitempty"`
}

// InterviewCancelRequest cancels an interview.
type InterviewCancelRequest struct {
	// Notify emails the candidate a calendar cancellation. Needs the company's reply-to address.
	Notify bool   `json:"notify,omitempty"`
	Note   string `json:"note,omitempty"`
}

// listInterviews godoc
//
//	@Summary		An application's interviews
//	@Description	Soonest first. Seen by people who can see the job.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	apphttp.InterviewsEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/interviews [get]
func (h *Handler) listInterviews(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Interviews(r.Context(), c, jobID(r), appID(r))
	finish(w, r, http.StatusOK, out, err)
}

// scheduleInterview godoc
//
//	@Summary		Schedule an interview
//	@Description	Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any. Interviewers must be on the job's team. With `invite` the candidate gets an email with a calendar event in the same step. At most 10 scheduled interviews per application. Not for decided applications.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			app_id	path		string						true	"Application id"
//	@Param			body	body		apphttp.InterviewRequest	true	"Interview"
//	@Success		201		{object}	apphttp.InterviewEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/interviews [post]
func (h *Handler) scheduleInterview(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req InterviewRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.ScheduleInterview(r.Context(), c, jobID(r), appID(r), applications.InterviewInput{
		StartsAt: req.StartsAt, DurationMin: req.DurationMinutes, Timezone: req.Timezone, Kind: req.Kind, Location: req.Location,
		Notes: req.Notes, InterviewerIDs: req.InterviewerIDs, Invite: req.Invite, InviteNote: req.InviteNote,
	})
	finish(w, r, http.StatusCreated, out, err)
}

// updateInterview godoc
//
//	@Summary		Change an interview
//	@Description	Reschedule it, change where it is or who takes it. Only a scheduled interview. With `invite` the candidate gets the updated calendar event, which replaces the earlier one.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id				path		string							true	"Job id"
//	@Param			app_id			path		string							true	"Application id"
//	@Param			interview_id	path		string							true	"Interview id"
//	@Param			body			body		apphttp.InterviewPatchRequest	true	"Changes"
//	@Success		200				{object}	apphttp.InterviewEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Failure		403				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Failure		409				{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/interviews/{interview_id} [patch]
func (h *Handler) updateInterview(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req InterviewPatchRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.UpdateInterview(r.Context(), c, jobID(r), appID(r), interviewID(r), applications.InterviewPatch{
		StartsAt: req.StartsAt, DurationMin: req.DurationMinutes, Timezone: req.Timezone, Kind: req.Kind, Location: req.Location,
		Notes: req.Notes, InterviewerIDs: req.InterviewerIDs, Invite: req.Invite, InviteNote: req.InviteNote,
	})
	finish(w, r, http.StatusOK, out, err)
}

// cancelInterview godoc
//
//	@Summary		Cancel an interview
//	@Description	Only a scheduled interview. With `notify` the candidate gets a calendar cancellation.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id				path		string								true	"Job id"
//	@Param			app_id			path		string								true	"Application id"
//	@Param			interview_id	path		string								true	"Interview id"
//	@Param			body			body		apphttp.InterviewCancelRequest		false	"Options"
//	@Success		200				{object}	apphttp.InterviewEnvelope
//	@Failure		403				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Failure		409				{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/interviews/{interview_id}/cancel [post]
func (h *Handler) cancelInterview(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req InterviewCancelRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CancelInterview(r.Context(), c, jobID(r), appID(r), interviewID(r), req.Notify, req.Note)
	finish(w, r, http.StatusOK, out, err)
}

// completeInterview godoc
//
//	@Summary		Mark an interview as held
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id				path		string	true	"Job id"
//	@Param			app_id			path		string	true	"Application id"
//	@Param			interview_id	path		string	true	"Interview id"
//	@Success		200				{object}	apphttp.InterviewEnvelope
//	@Failure		403				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Failure		409				{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/interviews/{interview_id}/complete [post]
func (h *Handler) completeInterview(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.CompleteInterview(r.Context(), c, jobID(r), appID(r), interviewID(r))
	finish(w, r, http.StatusOK, out, err)
}

func parseWhen(v string) (time.Time, bool) {
	if v == "" {
		return time.Time{}, true
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// mySchedule godoc
//
//	@Summary		My interviews
//	@Description	The scheduled interviews the caller takes, soonest first, across jobs they can see. From now to 30 days ahead by default.
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Param			from	query		string	false	"Start (RFC 3339 or YYYY-MM-DD)"
//	@Param			to		query		string	false	"End, exclusive (RFC 3339 or YYYY-MM-DD)"
//	@Param			limit	query		int		false	"At most this many (50 by default, 200 at most)"
//	@Success		200		{object}	apphttp.InterviewsEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/interviews/mine [get]
func (h *Handler) mySchedule(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	from, ok1 := parseWhen(q.Get("from"))
	to, ok2 := parseWhen(q.Get("to"))
	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "limit must be a positive number"))
			return
		}
		limit = n
	}
	if !ok1 || !ok2 {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "from and to must be RFC 3339 times or YYYY-MM-DD dates"))
		return
	}
	out, err := svc.MySchedule(r.Context(), c, from, to, limit)
	finish(w, r, http.StatusOK, out, err)
}

// compare godoc
//
//	@Summary		Compare applications
//	@Description	2 to 4 applications of the job side by side: each candidate's tags, files and interviews, and a row per question with every candidate's answer. Answers to special category questions are never included. Opening is audited as usual, but a comparison does not make the caller the reviewer of a flagged application.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id	path		string	true	"Job id"
//	@Param			ids	query		string	true	"Comma separated application ids (2 to 4)"
//	@Success		200	{object}	apphttp.CompareEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/compare [get]
func (h *Handler) compare(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var ids []string
	for _, p := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			ids = append(ids, p)
		}
	}
	out, err := svc.Compare(r.Context(), c, jobID(r), ids)
	finish(w, r, http.StatusOK, out, err)
}
