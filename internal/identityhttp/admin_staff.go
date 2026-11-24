package identityhttp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
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
//	@Security		AdminSessionCookie
//	@Success		200	{object}	identityhttp.AdminStaffListEnvelope
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
//	@Security		AdminSessionCookie
//	@Param			body	body		identityhttp.AdminCreateStaffRequest	true	"Invite payload"
//	@Success		201		{object}	identityhttp.AdminStaffEnvelope
//	@Router			/admin/staff [post]
func (h *AdminHandler) createStaff(w http.ResponseWriter, r *http.Request) {
	var body createStaffBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
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

	h.enqueueAdminInvite(r.Context(), result.Staff.Email, result.Staff.Name, result.RawToken)

	h.audit(r, "staff.invite", "staff", result.Staff.ID.String(), map[string]any{"roles": body.Roles})
	response.Success(w, r, http.StatusCreated, result.Staff)
}

// resendInvite godoc
//
//	@Summary		Resend staff invite
//	@Tags			admin/staff
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			id	path		string	true	"Staff UUID"
//	@Success		200	{object}	identityhttp.AdminStaffEnvelope
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

	h.enqueueAdminInvite(r.Context(), result.Staff.Email, result.Staff.Name, result.RawToken)

	h.audit(r, "staff.invite_resend", "staff", id.String(), nil)
	response.Success(w, r, http.StatusOK, result.Staff)
}

// InviteStaff creates the inactive staff user with the roles (slugs), mails the invite and audits it. actor is the
// inviting admin (uuid.Nil when unknown). It is shared by the HTTP route and the gRPC InviteStaff.
func (h *AdminHandler) InviteStaff(
	ctx context.Context, email, name string, roleSlugs []string, actor uuid.UUID,
) (admin.InviteResult, error) {
	result, err := h.Store.InviteStaff(ctx, email, name, roleSlugs, actor)
	if err != nil {
		return result, err
	}
	h.enqueueAdminInvite(ctx, result.Staff.Email, result.Staff.Name, result.RawToken)
	if actor != uuid.Nil {
		besteffort.Log(ctx, "Store.WriteAudit", h.Store.WriteAudit(ctx, actor, "staff.invite", "staff",
			result.Staff.ID.String(), map[string]any{"roles": roleSlugs}, ""))
	}
	return result, nil
}

func (h *AdminHandler) enqueueAdminInvite(ctx context.Context, email, name, rawToken string) {
	if h.Enqueuer == nil || h.GlobalDB == nil {
		slog.Warn("admin invite email skipped: enqueuer not configured", "email", email)
		return
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		slog.Error("admin invite email begin failed", "email", email, "error", err)
		return
	}
	defer func() { besteffort.Log(ctx, "tx.Rollback", tx.Rollback(ctx)) }()

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
//	@Security		AdminSessionCookie
//	@Param			id		path		string							true	"Staff UUID"
//	@Param			body	body		identityhttp.AdminPatchStaffRequest	true	"Patch payload"
//	@Success		200		{object}	identityhttp.AdminStaffEnvelope
//	@Router			/admin/staff/{id} [patch]
func (h *AdminHandler) patchStaff(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	var body patchStaffBody
	if decodeJSONErr := decodeJSON(r, &body); decodeJSONErr != nil {
		response.Error(w, r, apperror.InvalidBody(decodeJSONErr))
		return
	}

	staff, err := h.Store.UpdateStaff(r.Context(), id, body.Name, body.IsActive, body.Roles)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	// Deactivating a staff member ends all of their sessions at once.
	if body.IsActive != nil && !*body.IsActive {
		if revokeErr := h.Sessions.RevokeAll(r.Context(), id); revokeErr != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	}

	h.audit(r, "staff.patch", "staff", id.String(), nil)
	response.Success(w, r, http.StatusOK, staff)
}

// listAuditLogs godoc
//
//	@Summary		List admin audit logs
//	@Description	Newest first. Filter by actor, action, entity (resource_type and resource_id) and time. The ip is returned as stored. Page with limit and offset; meta.has_more says whether another page follows. Needs audit:read.
//	@Tags			admin/audit
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			actor_id		query	string	false	"Actor UUID"
//	@Param			action			query	string	false	"Action filter"
//	@Param			resource_type	query	string	false	"Resource type"
//	@Param			resource_id		query	string	false	"Resource id (with resource_type)"
//	@Param			from			query	string	false	"On or after this time (RFC 3339 or YYYY-MM-DD)"
//	@Param			to				query	string	false	"Before this time (RFC 3339), or up to the end of this day (YYYY-MM-DD)"
//	@Param			limit			query	int		false	"Page size (default 50, max 100)"
//	@Param			offset			query	int		false	"Offset"
//	@Success		200				{object}	identityhttp.AdminAuditListEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/audit-logs [get]
func (h *AdminHandler) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	qv := r.URL.Query()
	q := admin.AuditQuery{
		Action:       strings.TrimSpace(qv.Get("action")),
		ResourceType: strings.TrimSpace(qv.Get("resource_type")),
		ResourceID:   strings.TrimSpace(qv.Get("resource_id")),
	}
	if raw := strings.TrimSpace(qv.Get("actor_id")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid actor_id"))
			return
		}
		q.ActorID = &id
	}
	var err error
	if q.From, err = parseAuditTime(qv.Get("from"), false); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid from: use RFC 3339 or YYYY-MM-DD"))
		return
	}
	if q.To, err = parseAuditTime(qv.Get("to"), true); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid to: use RFC 3339 or YYYY-MM-DD"))
		return
	}
	if q.From != nil && q.To != nil && !q.From.Before(*q.To) {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "to must be after from"))
		return
	}
	limit := parseLimit(qv.Get("limit"), defaultPageLimit, maxPageLimit)
	if qv.Get("offset") != "" {
		q.Offset = parseLimit(qv.Get("offset"), 0, maxPageOffset)
	}
	// One extra row tells whether another page follows.
	q.Limit = limit + 1

	logs, err := h.Store.ListAudit(r.Context(), q)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	more := len(logs) > limit
	if more {
		logs = logs[:limit]
	}
	response.SuccessWithMeta(w, r, http.StatusOK, logs, &response.Meta{HasMore: &more})
}

func parseAuditTime(raw string, endOfDay bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		t = t.UTC()
		return &t, nil
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return nil, err
	}
	if endOfDay {
		t = t.AddDate(0, 0, 1)
	}
	return &t, nil
}
