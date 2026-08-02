package handler

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type createRoleBody struct {
	Slug        string   `json:"slug" validate:"required,min=2,max=64"`
	Name        string   `json:"name" validate:"required,min=1,max=120"`
	Permissions []string `json:"permissions" validate:"required,min=1,dive,required"`
}

type patchRoleBody struct {
	Name        *string   `json:"name"`
	Permissions *[]string `json:"permissions"`
}

// listRoles godoc
//
//	@Summary		List roles and permissions
//	@Tags			admin/roles
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

// createRole godoc
//
//	@Summary		Create role
//	@Tags			admin/roles
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AdminCreateRoleRequest	true	"Role payload"
//	@Success		201		{object}	handler.AdminRoleEnvelope
//	@Router			/admin/roles [post]
func (h *AdminHandler) createRole(w http.ResponseWriter, r *http.Request) {
	var body createRoleBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	body.Slug = strings.TrimSpace(strings.ToLower(body.Slug))
	body.Name = strings.TrimSpace(body.Name)
	if err := h.validate.Struct(body); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	role, err := h.Store.CreateRole(r.Context(), body.Slug, body.Name, body.Permissions)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "role.create", "role", role.ID.String(), map[string]any{
		"slug":        role.Slug,
		"permissions": body.Permissions,
	})
	response.Success(w, r, http.StatusCreated, role)
}

// patchRole godoc
//
//	@Summary		Update role
//	@Tags			admin/roles
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Role UUID"
//	@Param			body	body		handler.AdminPatchRoleRequest	true	"Patch payload"
//	@Success		200		{object}	handler.AdminRoleEnvelope
//	@Router			/admin/roles/{id} [patch]
func (h *AdminHandler) patchRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	var body patchRoleBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if body.Name == nil && body.Permissions == nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "no fields to update"))
		return
	}
	if body.Name != nil {
		trimmed := strings.TrimSpace(*body.Name)
		body.Name = &trimmed
	}

	role, err := h.Store.UpdateRole(r.Context(), id, body.Name, body.Permissions)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "role.update", "role", id.String(), nil)
	response.Success(w, r, http.StatusOK, role)
}

// deleteRole godoc
//
//	@Summary		Delete role
//	@Tags			admin/roles
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path	string	true	"Role UUID"
//	@Success		204
//	@Router			/admin/roles/{id} [delete]
func (h *AdminHandler) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	if err := h.Store.DeleteRole(r.Context(), id); err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "role.delete", "role", id.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// listPermissions godoc
//
//	@Summary		List permission catalog
//	@Tags			admin/roles
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.AdminPermissionsEnvelope
//	@Router			/admin/permissions [get]
func (h *AdminHandler) listPermissions(w http.ResponseWriter, r *http.Request) {
	perms, err := h.Store.ListPermissions(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, perms)
}
