package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/pkg/response"
)

const catalogCacheControl = "public, max-age=300"

func (h *ProductOnboardingHandler) writeCatalogList(w http.ResponseWriter, r *http.Request, load func(context.Context) (any, error)) {
	items, err := load(r.Context())
	if err != nil {
		response.Error(w, r, err)
		return
	}

	w.Header().Set("Cache-Control", catalogCacheControl)
	response.Success(w, r, http.StatusOK, items)
}

func (h *ProductOnboardingHandler) loadBusinessTypes(ctx context.Context) ([]BusinessType, error) {
	sql, args, err := query.ListBusinessTypes()
	if err != nil {
		return nil, fmt.Errorf("build list business types: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]BusinessType, 0, 8)
	for rows.Next() {
		var id uuid.UUID
		var item BusinessType
		if err := rows.Scan(&id, &item.Name, &item.Slug); err != nil {
			return nil, err
		}

		item.ID = id.String()
		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *ProductOnboardingHandler) loadOnboardingIndustries(ctx context.Context) ([]OnboardingIndustry, error) {
	sql, args, err := query.ListOnboardingIndustries()
	if err != nil {
		return nil, fmt.Errorf("build list onboarding industries: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]OnboardingIndustry, 0, 16)
	for rows.Next() {
		var id uuid.UUID
		var item OnboardingIndustry
		if err := rows.Scan(&id, &item.Name, &item.Slug, &item.Emoji); err != nil {
			return nil, err
		}

		item.ID = id.String()
		items = append(items, item)
	}

	return items, rows.Err()
}

func (h *ProductOnboardingHandler) loadCompanyRoles(ctx context.Context) ([]CompanyRole, error) {
	sql, args, err := query.ListCompanyRoles()
	if err != nil {
		return nil, fmt.Errorf("build list company roles: %w", err)
	}

	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]CompanyRole, 0, 8)
	for rows.Next() {
		var id uuid.UUID
		var item CompanyRole
		if err := rows.Scan(&id, &item.Name, &item.Slug, &item.IconKey); err != nil {
			return nil, err
		}

		item.ID = id.String()
		items = append(items, item)
	}

	return items, rows.Err()
}
