package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type adminWaitlistItem struct {
	UUID                  uuid.UUID       `json:"uuid"`
	FullName              *string         `json:"full_name,omitempty"`
	Email                 string          `json:"email"`
	CountryID             *uuid.UUID      `json:"country_id,omitempty"`
	Region                string          `json:"region"`
	RegionSource          string          `json:"region_source"`
	Metadata              json.RawMessage `json:"metadata"`
	OnboardingSubmittedAt *time.Time      `json:"onboarding_submitted_at,omitempty"`
	WantsEarlyAccess      *bool           `json:"wants_early_access,omitempty"`
	WantsUserTesting      *bool           `json:"wants_user_testing,omitempty"`
	RoleID                *uuid.UUID      `json:"role_id,omitempty"`
	TeamSizeID            *uuid.UUID      `json:"team_size_id,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	DeletedAt             *time.Time      `json:"deleted_at,omitempty"`
}

type adminWaitlistDetail struct {
	adminWaitlistItem
	IndustryIDs    []uuid.UUID `json:"industry_ids"`
	HiringToolIDs  []uuid.UUID `json:"hiring_tool_ids"`
	FrustrationIDs []uuid.UUID `json:"frustration_ids"`
}

type waitlistRegistryRow struct {
	Token  uuid.UUID
	Region region.Region
}

// listWaitlist godoc
//
//	@Summary		List waitlist entries
//	@Tags			admin/waitlist
//	@Produce		json
//	@Security		BearerAuth
//	@Param			region			query	string	false	"Region filter"
//	@Param			q				query	string	false	"Search email or name"
//	@Param			onboarding		query	string	false	"pending|complete"
//	@Param			include_deleted	query	bool	false	"Include soft-deleted"
//	@Param			limit			query	int		false	"Page size (max 100)"
//	@Success		200				{object}	handler.AdminWaitlistListEnvelope
//	@Failure		401				{object}	apidoc.ErrorEnvelope
//	@Failure		403				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/waitlist [get]
func (h *AdminHandler) listWaitlist(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	onboarding := strings.TrimSpace(r.URL.Query().Get("onboarding"))
	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	limit := parseLimit(r.URL.Query().Get("limit"), 50, 100)

	regionFilter := strings.TrimSpace(r.URL.Query().Get("region"))
	if regionFilter != "" {
		if _, err := region.ParseRegion(regionFilter); err != nil {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion))
			return
		}
	}

	registryRows, err := h.queryWaitlistRegistry(r.Context(), regionFilter, q, limit)
	if err != nil {
		response.Error(w, r, fmt.Errorf("list waitlist registry: %w", err))
		return
	}

	byRegion := map[region.Region][]uuid.UUID{}
	for _, row := range registryRows {
		byRegion[row.Region] = append(byRegion[row.Region], row.Token)
	}

	var items []adminWaitlistItem
	for reg, tokens := range byRegion {
		pool, err := h.Router.DB(reg)
		if err != nil {
			response.Error(w, r, err)
			return
		}
		part, err := queryWaitlistByUUIDs(r.Context(), pool, tokens, q, onboarding, includeDeleted)
		if err != nil {
			response.Error(w, r, fmt.Errorf("list waitlist %s: %w", reg, err))
			return
		}
		items = append(items, part...)
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	if items == nil {
		items = []adminWaitlistItem{}
	}

	response.Success(w, r, http.StatusOK, items)
}

// getWaitlist godoc
//
//	@Summary		Get waitlist entry
//	@Tags			admin/waitlist
//	@Produce		json
//	@Security		BearerAuth
//	@Param			uuid	path		string	true	"Waitlist UUID"
//	@Param			region	query		string	false	"Region hint"
//	@Success		200		{object}	handler.AdminWaitlistDetailEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/waitlist/{uuid} [get]
func (h *AdminHandler) getWaitlist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid uuid"))
		return
	}

	_, pool, err := h.findWaitlistRegion(r.Context(), id, r.URL.Query().Get("region"))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	detail, err := queryWaitlistDetail(r.Context(), pool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrNotFound)
			return
		}
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, detail)
}

type patchWaitlistBody struct {
	WantsEarlyAccess *bool           `json:"wants_early_access"`
	WantsUserTesting *bool           `json:"wants_user_testing"`
	Metadata         json.RawMessage `json:"metadata"`
}

// patchWaitlist godoc
//
//	@Summary		Update waitlist entry
//	@Tags			admin/waitlist
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			uuid	path		string						true	"Waitlist UUID"
//	@Param			region	query		string						false	"Region hint"
//	@Param			body	body		handler.AdminWaitlistPatchRequest	true	"Patch payload"
//	@Success		200		{object}	handler.AdminWaitlistDetailEnvelope
//	@Router			/admin/waitlist/{uuid} [patch]
func (h *AdminHandler) patchWaitlist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid uuid"))
		return
	}

	var body patchWaitlistBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	reg, pool, err := h.findWaitlistRegion(r.Context(), id, r.URL.Query().Get("region"))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	_, err = pool.Exec(r.Context(), `
		UPDATE waitlist.waitlist SET
			wants_early_access = COALESCE($2, wants_early_access),
			wants_user_testing = COALESCE($3, wants_user_testing),
			metadata = COALESCE($4, metadata),
			updated_at = now()
		WHERE uuid = $1
	`, id, body.WantsEarlyAccess, body.WantsUserTesting, nullableJSON(body.Metadata))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	detail, err := queryWaitlistDetail(r.Context(), pool, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "waitlist.patch", "waitlist", id.String(), map[string]any{"region": reg})
	response.Success(w, r, http.StatusOK, detail)
}

// deleteWaitlist godoc
//
//	@Summary		Soft-delete waitlist entry
//	@Tags			admin/waitlist
//	@Security		BearerAuth
//	@Param			uuid	path	string	true	"Waitlist UUID"
//	@Param			region	query	string	false	"Region hint"
//	@Success		204
//	@Router			/admin/waitlist/{uuid} [delete]
func (h *AdminHandler) deleteWaitlist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid uuid"))
		return
	}

	reg, pool, err := h.findWaitlistRegion(r.Context(), id, r.URL.Query().Get("region"))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	// Prefer UPDATE over DELETE: a BEFORE DELETE soft_delete_row trigger cancels the
	// physical delete (RETURN NULL), so RowsAffected can be 0 even when soft-delete succeeded.
	tag, err := pool.Exec(r.Context(), `
		UPDATE waitlist.waitlist SET deleted_at = now(), updated_at = now()
		WHERE uuid = $1 AND deleted_at IS NULL
	`, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	h.audit(r, "waitlist.delete", "waitlist", id.String(), map[string]any{"region": reg})
	w.WriteHeader(http.StatusNoContent)
}

// restoreWaitlist godoc
//
//	@Summary		Restore soft-deleted waitlist entry
//	@Tags			admin/waitlist
//	@Produce		json
//	@Security		BearerAuth
//	@Param			uuid	path		string	true	"Waitlist UUID"
//	@Param			region	query		string	false	"Region hint"
//	@Success		200		{object}	handler.AdminWaitlistDetailEnvelope
//	@Router			/admin/waitlist/{uuid}/restore [post]
func (h *AdminHandler) restoreWaitlist(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "uuid"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid uuid"))
		return
	}

	reg, pool, err := h.findWaitlistRegion(r.Context(), id, r.URL.Query().Get("region"))
	if err != nil {
		response.Error(w, r, err)
		return
	}

	tag, err := pool.Exec(r.Context(), `
		UPDATE waitlist.waitlist SET deleted_at = NULL, updated_at = now()
		WHERE uuid = $1 AND deleted_at IS NOT NULL
	`, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}

	detail, err := queryWaitlistDetail(r.Context(), pool, id)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	h.audit(r, "waitlist.restore", "waitlist", id.String(), map[string]any{"region": reg})
	response.Success(w, r, http.StatusOK, detail)
}

func (h *AdminHandler) findWaitlistRegion(ctx context.Context, id uuid.UUID, regionHint string) (region.Region, dbrouter.PgxPool, error) {
	var regStr string
	err := h.GlobalDB.QueryRow(ctx, `
		SELECT region FROM waitlist.registry WHERE waitlist_token = $1
	`, id).Scan(&regStr)
	if errors.Is(err, pgx.ErrNoRows) {
		return region.RegionUnknown, nil, apperror.ErrNotFound
	}
	if err != nil {
		return region.RegionUnknown, nil, err
	}

	reg, err := region.ParseRegion(regStr)
	if err != nil {
		return region.RegionUnknown, nil, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion)
	}

	if hint := strings.TrimSpace(regionHint); hint != "" {
		hintReg, err := region.ParseRegion(hint)
		if err != nil {
			return region.RegionUnknown, nil, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRegion)
		}
		if hintReg != reg {
			return region.RegionUnknown, nil, apperror.ErrNotFound
		}
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		return region.RegionUnknown, nil, err
	}
	return reg, pool, nil
}

func (h *AdminHandler) queryWaitlistRegistry(ctx context.Context, regionFilter, q string, limit int) ([]waitlistRegistryRow, error) {
	// Recent window (supports name search after regional hydrate).
	fetchLimit := limit * 5
	rows, err := h.GlobalDB.Query(ctx, `
		SELECT waitlist_token, region
		FROM waitlist.registry
		WHERE ($1 = '' OR region = $1)
		ORDER BY created_at DESC
		LIMIT $2
	`, regionFilter, fetchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := map[uuid.UUID]struct{}{}
	var out []waitlistRegistryRow
	appendRow := func(token uuid.UUID, regStr string) error {
		if _, ok := seen[token]; ok {
			return nil
		}
		reg, err := region.ParseRegion(regStr)
		if err != nil {
			return nil
		}
		seen[token] = struct{}{}
		out = append(out, waitlistRegistryRow{Token: token, Region: reg})
		return nil
	}

	for rows.Next() {
		var token uuid.UUID
		var regStr string
		if err := rows.Scan(&token, &regStr); err != nil {
			return nil, err
		}
		_ = appendRow(token, regStr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Exact email hits may be older than the recent window — pull them explicitly.
	if q != "" {
		emailRows, err := h.GlobalDB.Query(ctx, `
			SELECT waitlist_token, region
			FROM waitlist.registry
			WHERE ($1 = '' OR region = $1)
			  AND email ILIKE '%' || $2 || '%'
			ORDER BY created_at DESC
			LIMIT $3
		`, regionFilter, q, limit)
		if err != nil {
			return nil, err
		}
		defer emailRows.Close()

		for emailRows.Next() {
			var token uuid.UUID
			var regStr string
			if err := emailRows.Scan(&token, &regStr); err != nil {
				return nil, err
			}
			_ = appendRow(token, regStr)
		}
		if err := emailRows.Err(); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func queryWaitlistByUUIDs(
	ctx context.Context,
	pool dbrouter.PgxPool,
	tokens []uuid.UUID,
	q, onboarding string,
	includeDeleted bool,
) ([]adminWaitlistItem, error) {
	if len(tokens) == 0 {
		return nil, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT uuid, full_name, email, country_id, region, region_source, metadata,
			onboarding_submitted_at, wants_early_access, wants_user_testing,
			role_id, team_size_id, created_at, updated_at, deleted_at
		FROM waitlist.waitlist
		WHERE uuid = ANY($1)
		  AND ($2 = '' OR email ILIKE '%' || $2 || '%' OR full_name ILIKE '%' || $2 || '%')
		  AND ($3 = '' OR ($3 = 'pending' AND onboarding_submitted_at IS NULL) OR ($3 = 'complete' AND onboarding_submitted_at IS NOT NULL))
		  AND ($4 OR deleted_at IS NULL)
		ORDER BY created_at DESC
	`, tokens, q, onboarding, includeDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []adminWaitlistItem
	for rows.Next() {
		var item adminWaitlistItem
		if err := rows.Scan(
			&item.UUID, &item.FullName, &item.Email, &item.CountryID, &item.Region, &item.RegionSource, &item.Metadata,
			&item.OnboardingSubmittedAt, &item.WantsEarlyAccess, &item.WantsUserTesting,
			&item.RoleID, &item.TeamSizeID, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func queryWaitlistDetail(ctx context.Context, pool dbrouter.PgxPool, id uuid.UUID) (adminWaitlistDetail, error) {
	var d adminWaitlistDetail
	err := pool.QueryRow(ctx, `
		SELECT uuid, full_name, email, country_id, region, region_source, metadata,
			onboarding_submitted_at, wants_early_access, wants_user_testing,
			role_id, team_size_id, created_at, updated_at, deleted_at
		FROM waitlist.waitlist WHERE uuid = $1
	`, id).Scan(
		&d.UUID, &d.FullName, &d.Email, &d.CountryID, &d.Region, &d.RegionSource, &d.Metadata,
		&d.OnboardingSubmittedAt, &d.WantsEarlyAccess, &d.WantsUserTesting,
		&d.RoleID, &d.TeamSizeID, &d.CreatedAt, &d.UpdatedAt, &d.DeletedAt,
	)
	if err != nil {
		return adminWaitlistDetail{}, err
	}

	d.IndustryIDs, err = queryUUIDColumn(ctx, pool, `SELECT industry_id FROM waitlist.waitlist_industries wi JOIN waitlist.waitlist w ON w.id = wi.waitlist_id WHERE w.uuid = $1`, id)
	if err != nil {
		return adminWaitlistDetail{}, err
	}
	d.HiringToolIDs, err = queryUUIDColumn(ctx, pool, `SELECT hiring_tool_id FROM waitlist.waitlist_hiring_tools wi JOIN waitlist.waitlist w ON w.id = wi.waitlist_id WHERE w.uuid = $1`, id)
	if err != nil {
		return adminWaitlistDetail{}, err
	}
	d.FrustrationIDs, err = queryUUIDColumn(ctx, pool, `SELECT frustration_id FROM waitlist.waitlist_frustrations wi JOIN waitlist.waitlist w ON w.id = wi.waitlist_id WHERE w.uuid = $1`, id)
	if err != nil {
		return adminWaitlistDetail{}, err
	}
	if d.IndustryIDs == nil {
		d.IndustryIDs = []uuid.UUID{}
	}
	if d.HiringToolIDs == nil {
		d.HiringToolIDs = []uuid.UUID{}
	}
	if d.FrustrationIDs == nil {
		d.FrustrationIDs = []uuid.UUID{}
	}
	return d, nil
}

func queryUUIDColumn(ctx context.Context, pool dbrouter.PgxPool, sql string, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, sql, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var v uuid.UUID
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}
