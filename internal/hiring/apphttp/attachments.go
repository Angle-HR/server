package apphttp

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Envelopes for the API documentation.
type (
	// AttachmentsEnvelope is a list of uploaded files.
	AttachmentsEnvelope struct {
		Data []applications.Attachment `json:"data"`
	}
	// AttachmentEnvelope is one uploaded file.
	AttachmentEnvelope struct {
		Data applications.Attachment `json:"data"`
	}
)

func (h *Handler) registerAttachmentRoutes(r chi.Router) {
	r.Get("/applications/{app_id}/email-attachments", h.listAttachments)
	r.Post("/applications/{app_id}/email-attachments", h.uploadAttachment)
	r.Delete("/applications/{app_id}/email-attachments/{attachment_id}", h.deleteAttachment)
}

// listAttachments godoc
//
//	@Summary		Uploaded files waiting to be sent
//	@Description	Files uploaded for this candidate that no email uses yet.
//	@Tags			jobs
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Success		200		{object}	apphttp.AttachmentsEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		503		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/email-attachments [get]
func (h *Handler) listAttachments(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	out, err := svc.UnsentAttachments(r.Context(), c, jobID(r), appID(r))
	finish(w, r, http.StatusOK, out, err)
}

// uploadAttachment godoc
//
//	@Summary		Upload a file to send to a candidate
//	@Description	Multipart form with one `file` part. PDF, Word (.docx), PNG, JPEG or text, up to 5 MB; the content must match the name and passes a malware scan, or the file is refused. The file is not sent yet: pass its id in `attachment_ids` when sending an email (at most 3 files per email). Up to 10 files can wait on an application. Same permission as emailing the candidate.
//	@Tags			jobs
//	@Accept			mpfd
//	@Produce		json
//	@Security		SessionCookie
//	@Param			id		path		string	true	"Job id"
//	@Param			app_id	path		string	true	"Application id"
//	@Param			file	formData	file	true	"The file"
//	@Success		201		{object}	apphttp.AttachmentEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		413		{object}	apidoc.ErrorEnvelope
//	@Failure		422		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Failure		503		{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/email-attachments [post]
func (h *Handler) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, applications.MaxAttachmentBytes+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.Error(w, r, apperror.New(apperror.CodePayloadTooLarge, "the file is larger than 5 MB"))
		} else {
			response.Error(w, r, apperror.InvalidBody(err))
		}
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		response.Error(w, r, apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
			map[string]any{"fields": []map[string]string{{"path": "file", "message": "send the file in a form part named file"}}}))
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, applications.MaxAttachmentBytes+1))
	if err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	out, err := svc.UploadAttachment(r.Context(), c, jobID(r), appID(r), hdr.Filename, data)
	finish(w, r, http.StatusCreated, out, err)
}

// deleteAttachment godoc
//
//	@Summary		Remove an uploaded file
//	@Description	Only a file no email uses yet.
//	@Tags			jobs
//	@Security		SessionCookie
//	@Param			id				path	string	true	"Job id"
//	@Param			app_id			path	string	true	"Application id"
//	@Param			attachment_id	path	string	true	"File id"
//	@Success		204
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/applications/{app_id}/email-attachments/{attachment_id} [delete]
func (h *Handler) deleteAttachment(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.appScope(w, r)
	if !ok {
		return
	}
	if err := svc.DeleteAttachment(r.Context(), c, jobID(r), appID(r), chi.URLParam(r, "attachment_id")); err != nil {
		response.Error(w, r, applicationsError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
