package apphttp

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Envelopes for the API documentation.
type (
	// StagesEnvelope is a job's pipeline.
	StagesEnvelope struct {
		Data []applications.Stage `json:"data"`
	}
	// StageEnvelope is one stage.
	StageEnvelope struct {
		Data applications.Stage `json:"data"`
	}
	// MoveResultEnvelope is the outcome of a stage move.
	MoveResultEnvelope struct {
		Data applications.MoveResult `json:"data"`
	}
	// BulkMoveEnvelope is the outcome of a bulk stage move, one item per application.
	BulkMoveEnvelope struct {
		Data []applications.BulkItem `json:"data"`
	}
	// NotesEnvelope is an application's notes.
	NotesEnvelope struct {
		Data []applications.Note `json:"data"`
	}
	// NoteEnvelope is one note.
	NoteEnvelope struct {
		Data applications.Note `json:"data"`
	}
	// TagsEnvelope is a list of tags.
	TagsEnvelope struct {
		Data []applications.Tag `json:"data"`
	}
	// TagEnvelope is one tag.
	TagEnvelope struct {
		Data applications.Tag `json:"data"`
	}
	// ActivityEnvelope is one page of an application's timeline.
	ActivityEnvelope struct {
		Data applications.ActivityPage `json:"data"`
	}
	// ReasonsEnvelope is the disqualification reasons.
	ReasonsEnvelope struct {
		Data []applications.Reason `json:"data"`
	}
)

// registerPipelineJobRoutes mounts the pipeline routes of a job. r is the router of /jobs/{id}.
func (h *Handler) registerPipelineJobRoutes(r chi.Router) {
	r.Get("/stages", h.listStages)
	r.Post("/stages", h.createStage)
	r.Put("/stages/order", h.reorderStages)
	r.Patch("/stages/{stage_id}", h.renameStage)
	r.Delete("/stages/{stage_id}", h.deleteStage)
	r.Post("/applications/bulk/stage", h.bulkMoveStage)
	r.Post("/applications/{app_id}/stage", h.moveStage)
	r.Get("/applications/{app_id}/notes", h.listNotes)
	r.Post("/applications/{app_id}/notes", h.addNote)
	r.Patch("/applications/{app_id}/notes/{note_id}", h.editNote)
	r.Delete("/applications/{app_id}/notes/{note_id}", h.deleteNote)
	r.Put("/applications/{app_id}/tags", h.setTags)
	r.Put("/applications/{app_id}/follow", h.follow)
	r.Delete("/applications/{app_id}/follow", h.unfollow)
	r.Get("/applications/{app_id}/activity", h.activity)
}

// registerPipelineCompanyRoutes mounts the company-level pipeline routes. r is the router of /hiring.
func (h *Handler) registerPipelineCompanyRoutes(r chi.Router) {
	r.Get("/tags", h.listTags)
	r.Post("/tags", h.createTag)
	r.Delete("/tags/{tag_id}", h.deleteTag)
	r.Get("/disqualification-reasons", h.reasons)
}

func stageID(r *http.Request) string { return chi.URLParam(r, "stage_id") }
func noteID(r *http.Request) string  { return chi.URLParam(r, "note_id") }

// decodeBody reads and strictly decodes a JSON body; it answers the error itself.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, ok := readBody(w, r)
	if !ok {
		return false
	}
	if err := decodeStrict(raw, dst); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return false
	}
	return true
}

// finish answers err through applicationsError, or the data on success.
func finish(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	noStore(w)
	response.Success(w, r, status, data)
}

// listStages godoc
//
//	@Summary		A job's pipeline stages
//	@Description	Applied, Shortlisted, Interview, Offer, Hired and Disqualified, plus the company's custom stages, in order, each with the number of applications in it. The default stages are created the first time they are needed.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	apphttp.StagesEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/stages [get]
func (h *Handler) listStages(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Stages(r.Context(), c, jobID(r))
	finish(w, r, http.StatusOK, out, err)
}

// StageNameRequest names a stage.
type StageNameRequest struct {
	Name string `json:"name"`
}

// createStage godoc
//
//	@Summary		Add a custom stage
//	@Description	The new stage goes right before Hired. A job has at most 20 stages and names are unique within it. Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			body	body		apphttp.StageNameRequest	true	"Stage"
//	@Success		201		{object}	apphttp.StageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/stages [post]
func (h *Handler) createStage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req StageNameRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CreateStage(r.Context(), c, jobID(r), req.Name)
	finish(w, r, http.StatusCreated, out, err)
}

// renameStage godoc
//
//	@Summary		Rename a stage
//	@Description	Applied, Hired and Disqualified keep their names.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id			path		string						true	"Job id"
//	@Param			stage_id	path		string						true	"Stage id"
//	@Param			body		body		apphttp.StageNameRequest	true	"Name"
//	@Success		200			{object}	apphttp.StagesEnvelope
//	@Failure		400			{object}	apidoc.ErrorEnvelope
//	@Failure		403			{object}	apidoc.ErrorEnvelope
//	@Failure		404			{object}	apidoc.ErrorEnvelope
//	@Failure		409			{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/stages/{stage_id} [patch]
func (h *Handler) renameStage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req StageNameRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := svc.RenameStage(r.Context(), c, jobID(r), stageID(r), req.Name); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	out, err := svc.Stages(r.Context(), c, jobID(r))
	finish(w, r, http.StatusOK, out, err)
}

// deleteStage godoc
//
//	@Summary		Remove a custom stage
//	@Description	Only an empty custom stage can be removed.
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id			path	string	true	"Job id"
//	@Param			stage_id	path	string	true	"Stage id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/stages/{stage_id} [delete]
func (h *Handler) deleteStage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteStage(r.Context(), c, jobID(r), stageID(r)); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// StageOrderRequest lists the open stages in their new order.
type StageOrderRequest struct {
	StageIDs []string `json:"stage_ids"`
}

// reorderStages godoc
//
//	@Summary		Reorder the open stages
//	@Description	List every open stage once with Applied first. Hired and Disqualified always stay last. Returns the pipeline.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			body	body		apphttp.StageOrderRequest	true	"Order"
//	@Success		200		{object}	apphttp.StagesEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/stages/order [put]
func (h *Handler) reorderStages(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req StageOrderRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if err := svc.ReorderStages(r.Context(), c, jobID(r), req.StageIDs); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	out, err := svc.Stages(r.Context(), c, jobID(r))
	finish(w, r, http.StatusOK, out, err)
}

// MoveStageRequest moves an application to a stage.
type MoveStageRequest struct {
	StageID string `json:"stage_id"`
	// Reason is a disqualification reason code (GET /hiring/disqualification-reasons); needed for Disqualified.
	Reason string `json:"reason,omitempty"`
	Note   string `json:"note,omitempty"`
}

// moveStage godoc
//
//	@Summary		Move an application to a stage
//	@Description	To an open stage, a received application becomes in_review (unless the stage is Applied). To Hired or Disqualified the application is decided: it ends, its retention clock starts and the candidate is emailed unless the company turned that off. Disqualified needs a reason. A flagged application is moved only after its review; a decided one never moves.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string						true	"Job id"
//	@Param			app_id	path		string						true	"Application id"
//	@Param			body	body		apphttp.MoveStageRequest	true	"Stage"
//	@Success		200		{object}	apphttp.MoveResultEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/stage [post]
func (h *Handler) moveStage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req MoveStageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.MoveStage(r.Context(), c, jobID(r), appID(r), applications.MoveInput{StageID: req.StageID, Reason: req.Reason, Note: req.Note})
	finish(w, r, http.StatusOK, out, err)
}

// BulkMoveStageRequest moves several applications to one stage.
type BulkMoveStageRequest struct {
	ApplicationIDs []string `json:"application_ids"`
	StageID        string   `json:"stage_id"`
	Reason         string   `json:"reason,omitempty"`
	Note           string   `json:"note,omitempty"`
}

// bulkMoveStage godoc
//
//	@Summary		Move several applications to a stage
//	@Description	Up to 100 applications. Each is its own change; the answer says per application whether it moved, and why not (not_found, flagged, final, changed). Flagged applications are never moved in bulk: they are decided one by one through their review.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string							true	"Job id"
//	@Param			body	body		apphttp.BulkMoveStageRequest	true	"Applications and stage"
//	@Success		200		{object}	apphttp.BulkMoveEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/bulk/stage [post]
func (h *Handler) bulkMoveStage(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req BulkMoveStageRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.BulkMoveStage(r.Context(), c, jobID(r), req.ApplicationIDs,
		applications.MoveInput{StageID: req.StageID, Reason: req.Reason, Note: req.Note})
	finish(w, r, http.StatusOK, out, err)
}

// listNotes godoc
//
//	@Summary		An application's notes
//	@Description	Newest first. Internal: candidates never see them. Seen by people who can see the job.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	apphttp.NotesEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/notes [get]
func (h *Handler) listNotes(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Notes(r.Context(), c, jobID(r), appID(r))
	finish(w, r, http.StatusOK, out, err)
}

// NoteRequest is the text of a note.
type NoteRequest struct {
	Body string `json:"body"`
}

// addNote godoc
//
//	@Summary		Add a note
//	@Description	Anyone who can see the job can add one; interviewers leave their feedback this way. At most 4,000 characters.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string				true	"Job id"
//	@Param			app_id	path		string				true	"Application id"
//	@Param			body	body		apphttp.NoteRequest	true	"Note"
//	@Success		201		{object}	apphttp.NoteEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/notes [post]
func (h *Handler) addNote(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req NoteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.AddNote(r.Context(), c, jobID(r), appID(r), req.Body)
	finish(w, r, http.StatusCreated, out, err)
}

// editNote godoc
//
//	@Summary		Edit a note
//	@Description	Only the author can.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string				true	"Job id"
//	@Param			app_id	path		string				true	"Application id"
//	@Param			note_id	path		string				true	"Note id"
//	@Param			body	body		apphttp.NoteRequest	true	"Note"
//	@Success		200		{object}	apphttp.NoteEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/notes/{note_id} [patch]
func (h *Handler) editNote(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req NoteRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.EditNote(r.Context(), c, jobID(r), appID(r), noteID(r), req.Body)
	finish(w, r, http.StatusOK, out, err)
}

// deleteNote godoc
//
//	@Summary		Delete a note
//	@Description	Only the author can.
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id		path	string	true	"Job id"
//	@Param			app_id	path	string	true	"Application id"
//	@Param			note_id	path	string	true	"Note id"
//	@Success		204
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/notes/{note_id} [delete]
func (h *Handler) deleteNote(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteNote(r.Context(), c, jobID(r), appID(r), noteID(r)); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ApplicationTagsRequest replaces an application's tags.
type ApplicationTagsRequest struct {
	TagIDs []string `json:"tag_ids"`
}

// setTags godoc
//
//	@Summary		Set an application's tags
//	@Description	Replaces the tags: send the full list. At most 10. Needs to be the job's creator, a hiring manager or recruiter on it, or job.manage_any.
//	@Tags			jobs
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string							true	"Job id"
//	@Param			app_id	path		string							true	"Application id"
//	@Param			body	body		apphttp.ApplicationTagsRequest	true	"Tags"
//	@Success		200		{object}	apphttp.TagsEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/tags [put]
func (h *Handler) setTags(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req ApplicationTagsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.SetTags(r.Context(), c, jobID(r), appID(r), req.TagIDs)
	finish(w, r, http.StatusOK, out, err)
}

// follow godoc
//
//	@Summary		Follow an application
//	@Description	Stars the application for the caller. Repeating it is harmless.
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id		path	string	true	"Job id"
//	@Param			app_id	path	string	true	"Application id"
//	@Success		204
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/follow [put]
func (h *Handler) follow(w http.ResponseWriter, r *http.Request) { h.setFollow(w, r, true) }

// unfollow godoc
//
//	@Summary		Unfollow an application
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id		path	string	true	"Job id"
//	@Param			app_id	path	string	true	"Application id"
//	@Success		204
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/follow [delete]
func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) { h.setFollow(w, r, false) }

func (h *Handler) setFollow(w http.ResponseWriter, r *http.Request, on bool) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.Follow(r.Context(), c, jobID(r), appID(r), on); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// activity godoc
//
//	@Summary		An application's activity
//	@Description	The timeline, newest first: arrival, stage and status changes, notes added, edited or deleted (never their text) and tag changes. 30 per page by default (100 at most).
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Param			cursor	query		string	false	"next_cursor of the previous page"
//	@Param			limit	query		int		false	"Page size"
//	@Success		200		{object}	apphttp.ActivityEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/activity [get]
func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
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
	out, err := svc.Activity(r.Context(), c, jobID(r), appID(r), r.URL.Query().Get("cursor"), limit)
	finish(w, r, http.StatusOK, out, err)
}

// listTags godoc
//
//	@Summary		The company's tags
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	apphttp.TagsEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/tags [get]
func (h *Handler) listTags(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.Tags(r.Context(), c)
	finish(w, r, http.StatusOK, out, err)
}

// TagRequest creates a tag.
type TagRequest struct {
	Name string `json:"name"`
	// Color is gray (default), red, orange, yellow, green, teal, blue, purple or pink.
	Color string `json:"color,omitempty"`
}

// createTag godoc
//
//	@Summary		Create a tag
//	@Description	Needs job.manage_any. Names are unique, ignoring case.
//	@Tags			hiring
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		apphttp.TagRequest	true	"Tag"
//	@Success		201		{object}	apphttp.TagEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/tags [post]
func (h *Handler) createTag(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	var req TagRequest
	if !decodeBody(w, r, &req) {
		return
	}
	out, err := svc.CreateTag(r.Context(), c, req.Name, req.Color)
	finish(w, r, http.StatusCreated, out, err)
}

// deleteTag godoc
//
//	@Summary		Delete a tag
//	@Description	Needs job.manage_any. The tag is removed from every application.
//	@Tags			hiring
//	@Security		SessionCookie
//	@Param			tag_id	path	string	true	"Tag id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/hiring/tags/{tag_id} [delete]
func (h *Handler) deleteTag(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteTag(r.Context(), c, chi.URLParam(r, "tag_id")); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reasons godoc
//
//	@Summary		Disqualification reasons
//	@Description	The codes a rejection or a move to Disqualified takes as `reason`.
//	@Tags			hiring
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	apphttp.ReasonsEnvelope
//	@Router			/hiring/disqualification-reasons [get]
func (h *Handler) reasons(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	response.Success(w, r, http.StatusOK, applications.DisqualifyReasons)
}
