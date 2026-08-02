package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type createStaffBody struct {
	Email    string   `json:"email" validate:"required,email,max=254"`
	Password string   `json:"password" validate:"required,min=8,max=128"`
	Name     string   `json:"name" validate:"max=120"`
	Roles    []string `json:"roles" validate:"required,min=1,dive,required"`
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
//	@Summary		Create admin staff
//	@Tags			admin/staff
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AdminCreateStaffRequest	true	"Staff payload"
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

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	user, err := h.Store.CreateUser(r.Context(), body.Email, hash, body.Name, true)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	for _, role := range body.Roles {
		if err := h.Store.AssignRoleBySlug(r.Context(), user.ID, role); err != nil {
			response.Error(w, r, err)
			return
		}
	}

	roles := append([]string(nil), body.Roles...)
	staff := admin.StaffMember{
		ID:        user.ID,
		Email:     user.Email,
		Name:      user.Name,
		IsActive:  user.IsActive,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
		Roles:     roles,
	}

	h.audit(r, "staff.create", "staff", user.ID.String(), map[string]any{"roles": body.Roles})
	response.Success(w, r, http.StatusCreated, staff)
}

// patchStaff godoc
//
//	@Summary		Update admin staff
//	@Tags			admin/staff
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Staff UUID"
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

// listRoles godoc
//
//	@Summary		List roles and permissions
//	@Tags			admin/staff
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AdminRolesEnvelope
//	@Router			/admin/roles [get]
func (h *AdminHandler) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := h.Store.ListRolesWithPermissions(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, roles)
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
