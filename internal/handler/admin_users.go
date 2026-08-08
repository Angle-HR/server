package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type adminUserListItem struct {
	ID        uuid.UUID  `json:"id"`
	Email     string     `json:"email"`
	Region    string     `json:"region"`
	UserID    *uuid.UUID `json:"user_id,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type adminUserDetail struct {
	RegistryID            uuid.UUID  `json:"registry_id"`
	Email                 string     `json:"email"`
	Region                string     `json:"region"`
	RegionSource          string     `json:"region_source"`
	UserID                *uuid.UUID `json:"user_id,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	EmailVerifiedAt       *time.Time `json:"email_verified_at,omitempty"`
	OnboardingCompletedAt *time.Time `json:"onboarding_completed_at,omitempty"`
	AccountType           *string    `json:"account_type,omitempty"`
	FirstName             *string    `json:"first_name,omitempty"`
	LastName              *string    `json:"last_name,omitempty"`
	LegalFullName         *string    `json:"legal_full_name,omitempty"`
	CountryID             *uuid.UUID `json:"country_id,omitempty"`
	DeletedAt             *time.Time `json:"deleted_at,omitempty"`
	OnboardingStep        *string    `json:"onboarding_step,omitempty"`
	CompletedSteps        []string   `json:"completed_steps,omitempty"`
	OrganizationLegalName *string    `json:"organization_legal_name,omitempty"`
}

// listUsers godoc
//
//	@Summary		List product users
//	@Tags			admin/users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			region	query	string	false	"Region filter"
//	@Param			q		query	string	false	"Search email"
//	@Param			limit	query	int		false	"Page size"
//	@Param			offset	query	int		false	"Offset"
//	@Success		200		{object}	handler.AdminUserListEnvelope
//	@Router			/admin/users [get]
func (h *AdminHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 100)
	offset := parseLimit(r.URL.Query().Get("offset"), 0, 100000)
	if r.URL.Query().Get("offset") == "" {
		offset = 0
	}

	regionFilter := strings.TrimSpace(r.URL.Query().Get("region"))
	if regionFilter != "" {
		if _, err := region.ParseRegion(regionFilter); err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion))
			return
		}
	}

	rows, err := h.GlobalDB.Query(r.Context(), `
		SELECT id, email, region, user_id, created_at, updated_at
		FROM auth.users_registry
		WHERE user_id IS NOT NULL
		  AND ($1 = '' OR email ILIKE '%' || $1 || '%')
		  AND ($2 = '' OR region = $2)
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4
	`, q, regionFilter, limit, offset)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	defer rows.Close()

	var items []adminUserListItem
	for rows.Next() {
		var item adminUserListItem
		if err := rows.Scan(&item.ID, &item.Email, &item.Region, &item.UserID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			response.Error(w, r, err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	if items == nil {
		items = []adminUserListItem{}
	}
	response.Success(w, r, http.StatusOK, items)
}

// getUser godoc
//
//	@Summary		Get product user detail
//	@Tags			admin/users
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Registry UUID or regional user UUID"
//	@Success		200	{object}	handler.AdminUserDetailEnvelope
//	@Router			/admin/users/{id} [get]
func (h *AdminHandler) getUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	detail, err := h.loadUserDetail(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, detail)
}

type patchUserBody struct {
	Deleted *bool `json:"deleted"`
}

// patchUser godoc
//
//	@Summary		Update product user
//	@Description	Soft-delete or restore a regional account (`deleted: true|false`).
//	@Tags			admin/users
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string						true	"Registry UUID"
//	@Param			body	body		handler.AdminUserPatchRequest	true	"Patch payload"
//	@Success		200		{object}	handler.AdminUserDetailEnvelope
//	@Router			/admin/users/{id} [patch]
func (h *AdminHandler) patchUser(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}

	var body patchUserBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if body.Deleted == nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "deleted is required"))
		return
	}

	detail, err := h.loadUserDetail(r.Context(), id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if detail.UserID == nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "user has no regional account yet"))
		return
	}

	reg, err := region.ParseRegion(detail.Region)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	if *body.Deleted {
		_, err = pool.Exec(r.Context(), `
			UPDATE accounts.users SET deleted_at = COALESCE(deleted_at, now()), updated_at = now()
			WHERE id = $1
		`, *detail.UserID)
	} else {
		_, err = pool.Exec(r.Context(), `
			UPDATE accounts.users SET deleted_at = NULL, updated_at = now()
			WHERE id = $1
		`, *detail.UserID)
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}

	updated, err := h.loadUserDetail(r.Context(), detail.RegistryID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "users.patch", "user", detail.RegistryID.String(), map[string]any{"deleted": *body.Deleted})
	response.Success(w, r, http.StatusOK, updated)
}

func (h *AdminHandler) loadUserDetail(ctx context.Context, id uuid.UUID) (adminUserDetail, error) {
	var d adminUserDetail
	err := h.GlobalDB.QueryRow(ctx, `
		SELECT id, email, region, region_source, user_id, created_at, updated_at
		FROM auth.users_registry
		WHERE id = $1 OR user_id = $1
	`, id).Scan(
		&d.RegistryID, &d.Email, &d.Region, &d.RegionSource, &d.UserID, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminUserDetail{}, apperror.ErrNotFound
	}
	if err != nil {
		return adminUserDetail{}, err
	}

	if d.UserID == nil {
		return d, nil
	}

	reg, err := region.ParseRegion(d.Region)
	if err != nil {
		return d, nil
	}
	pool, err := h.Router.DB(reg)
	if err != nil {
		return adminUserDetail{}, err
	}

	err = pool.QueryRow(ctx, `
		SELECT email_verified_at, onboarding_completed_at, account_type, first_name, last_name,
			legal_full_name, country_id, deleted_at
		FROM accounts.users WHERE id = $1
	`, *d.UserID).Scan(
		&d.EmailVerifiedAt, &d.OnboardingCompletedAt, &d.AccountType, &d.FirstName, &d.LastName,
		&d.LegalFullName, &d.CountryID, &d.DeletedAt,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return adminUserDetail{}, fmt.Errorf("load regional user: %w", err)
	}

	_ = pool.QueryRow(ctx, `
		SELECT current_step, completed_steps FROM accounts.onboarding_progress WHERE user_id = $1
	`, *d.UserID).Scan(&d.OnboardingStep, &d.CompletedSteps)

	_ = pool.QueryRow(ctx, `
		SELECT legal_name FROM accounts.organizations WHERE owner_user_id = $1
	`, *d.UserID).Scan(&d.OrganizationLegalName)

	return d, nil
}
