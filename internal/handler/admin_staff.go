package handler

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type createStaffBody struct {
	Email string   `json:"email" validate:"required,email,max=254"`
	Name  string   `json:"name" validate:"max=120"`
	Roles []string `json:"roles" validate:"required,min=1,dive,required"`
}

type patchStaffBody struct {
	Name     *string   `json:"name"`
	IsActive *bool     `json:"is_active"`
	Roles    *[]string `json:"roles"`
}

// listStaff godoc
//
//	@Summary		List admin staff
//	@Tags			admin/staff
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AdminStaffListEnvelope
//	@Router			/admin/staff [get]
func (h *AdminHandler) listStaff(w http.ResponseWriter, r *http.Request) {
	items, err := h.Store.ListStaff(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, items)
}

// createStaff godoc
//
//	@Summary		Invite admin staff
//	@Description	Creates an inactive staff user and emails an invite token to set name and password.
//	@Tags			admin/staff
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AdminCreateStaffRequest	true	"Invite payload"
//	@Success		201		{object}	handler.AdminStaffEnvelope
//	@Router			/admin/staff [post]
func (h *AdminHandler) createStaff(w http.ResponseWriter, r *http.Request) {
	var body createStaffBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	body.Email = strings.TrimSpace(strings.ToLower(body.Email))
	body.Name = strings.TrimSpace(body.Name)
	if err := h.validate.Struct(body); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	actorID, _, _, _ := auth.AdminFromContext(r.Context())
	result, err := h.Store.InviteStaff(r.Context(), body.Email, body.Name, body.Roles, actorID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.enqueueAdminInvite(r, result.Staff.Email, result.Staff.Name, result.RawToken)

	h.audit(r, "staff.invite", "staff", result.Staff.ID.String(), map[string]any{"roles": body.Roles})
	response.Success(w, r, http.StatusCreated, result.Staff)
}

// resendInvite godoc
//
//	@Summary		Resend staff invite
//	@Tags			admin/staff
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Staff UUID"
//	@Success		200	{object}	handler.AdminStaffEnvelope
//	@Router			/admin/staff/{id}/resend-invite [post]
func (h *AdminHandler) resendInvite(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	result, err := h.Store.ResendInvite(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.enqueueAdminInvite(r, result.Staff.Email, result.Staff.Name, result.RawToken)

	h.audit(r, "staff.invite_resend", "staff", id.String(), nil)
	response.Success(w, r, http.StatusOK, result.Staff)
}

func (h *AdminHandler) enqueueAdminInvite(r *http.Request, email, name, rawToken string) {
	if h.Enqueuer == nil || h.GlobalDB == nil {
		slog.Warn("admin invite email skipped: enqueuer not configured", "email", email)
		return
	}
	ctx := r.Context()
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		slog.Error("admin invite email begin failed", "email", email, "error", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	displayName := name
	if displayName == "" {
		displayName = email
	}
	_, err = h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:             mailer.TypeAdminInvite,
		Recipient:        email,
		FullName:         displayName,
		Token:            rawToken,
		ExpiresInSeconds: int(admin.InviteTTL.Seconds()),
	}, queue.EmailEnqueueOptions()...)
	if err != nil {
		slog.Error("admin invite email enqueue failed", "email", email, "error", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("admin invite email commit failed", "email", email, "error", err)
	}
}

// patchStaff godoc
//
//	@Summary		Update admin staff
//	@Tags			admin/staff
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string							true	"Staff UUID"
//	@Param			body	body		handler.AdminPatchStaffRequest	true	"Patch payload"
//	@Success		200		{object}	handler.AdminStaffEnvelope
//	@Router			/admin/staff/{id} [patch]
func (h *AdminHandler) patchStaff(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	var body patchStaffBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	staff, err := h.Store.UpdateStaff(r.Context(), id, body.Name, body.IsActive, body.Roles)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "staff.patch", "staff", id.String(), nil)
	response.Success(w, r, http.StatusOK, staff)
}

// listAuditLogs godoc
//
//	@Summary		List admin audit logs
//	@Tags			admin/audit
//	@Produce		json
//	@Security		BearerAuth
//	@Param			actor_id		query	string	false	"Actor UUID"
//	@Param			action			query	string	false	"Action filter"
//	@Param			resource_type	query	string	false	"Resource type"
//	@Param			limit			query	int		false	"Page size"
//	@Param			offset			query	int		false	"Offset"
//	@Success		200				{object}	handler.AdminAuditListEnvelope
//	@Router			/admin/audit-logs [get]
func (h *AdminHandler) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	var actorID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("actor_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid actor_id"))
			return
		}
		actorID = &id
	}
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 100)
	offset := parseLimit(r.URL.Query().Get("offset"), 0, 100000)
	if r.URL.Query().Get("offset") == "" {
		offset = 0
	}

	logs, err := h.Store.ListAuditLogs(
		r.Context(),
		actorID,
		strings.TrimSpace(r.URL.Query().Get("action")),
		strings.TrimSpace(r.URL.Query().Get("resource_type")),
		limit,
		offset,
	)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, logs)
}
