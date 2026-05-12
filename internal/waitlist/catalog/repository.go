package catalog

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/db/sqlc"
	"github.com/Angle-HR/server/pkg/db"
)

// Industry is an active industry option.
type Industry struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	IconKey   string    `json:"icon_key,omitempty"`
	SortOrder int       `json:"sort_order"`
}

// HiringTool is an active hiring tool option.
type HiringTool struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	IconKey   string    `json:"icon_key,omitempty"`
	Category  string    `json:"category,omitempty"`
	SortOrder int       `json:"sort_order"`
}

// HiringFrustration is an active hiring frustration option.
type HiringFrustration struct {
	ID          uuid.UUID `json:"id"`
	Description string    `json:"description"`
	Slug        string    `json:"slug"`
	SortOrder   int       `json:"sort_order"`
}

// Role is an active role option.
type Role struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	SortOrder int       `json:"sort_order"`
}

// TeamSize is a team size band option.
type TeamSize struct {
	ID        uuid.UUID `json:"id"`
	Label     string    `json:"label"`
	MinSize   *int      `json:"min_size,omitempty"`
	MaxSize   *int      `json:"max_size,omitempty"`
	SortOrder int       `json:"sort_order"`
}

// Repository loads onboarding reference data.
type Repository struct {
	queries *sqlc.Queries
}

// NewRepository returns a catalog repository backed by PostgreSQL.
func NewRepository(queries *sqlc.Queries) *Repository {
	return &Repository{queries: queries}
}

// ListIndustries returns active industries ordered for display.
func (r *Repository) ListIndustries(ctx context.Context) ([]Industry, error) {
	rows, err := r.queries.ListIndustries(ctx)
	if err != nil {
		return nil, fmt.Errorf("list industries: %w", err)
	}

	items := make([]Industry, 0, len(rows))
	for _, row := range rows {
		items = append(items, Industry{
			ID:        row.Uuid,
			Name:      row.Name,
			Slug:      row.Slug,
			IconKey:   row.IconKey,
			SortOrder: int(row.SortOrder),
		})
	}

	return items, nil
}

// ListHiringTools returns active hiring tools ordered for display.
func (r *Repository) ListHiringTools(ctx context.Context) ([]HiringTool, error) {
	rows, err := r.queries.ListHiringTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hiring tools: %w", err)
	}

	items := make([]HiringTool, 0, len(rows))
	for _, row := range rows {
		items = append(items, HiringTool{
			ID:        row.Uuid,
			Name:      row.Name,
			Slug:      row.Slug,
			IconKey:   row.IconKey,
			Category:  row.Category,
			SortOrder: int(row.SortOrder),
		})
	}

	return items, nil
}

// ListHiringFrustrations returns active frustrations ordered for display.
func (r *Repository) ListHiringFrustrations(ctx context.Context) ([]HiringFrustration, error) {
	rows, err := r.queries.ListHiringFrustrations(ctx)
	if err != nil {
		return nil, fmt.Errorf("list hiring frustrations: %w", err)
	}

	items := make([]HiringFrustration, 0, len(rows))
	for _, row := range rows {
		items = append(items, HiringFrustration{
			ID:          row.Uuid,
			Description: row.Description,
			Slug:        row.Slug,
			SortOrder:   int(row.SortOrder),
		})
	}

	return items, nil
}

// ListRoles returns active roles ordered for display.
func (r *Repository) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := r.queries.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}

	items := make([]Role, 0, len(rows))
	for _, row := range rows {
		items = append(items, Role{
			ID:        row.Uuid,
			Name:      row.Name,
			Slug:      row.Slug,
			SortOrder: int(row.SortOrder),
		})
	}

	return items, nil
}

// ListTeamSizes returns team size bands ordered for display.
func (r *Repository) ListTeamSizes(ctx context.Context) ([]TeamSize, error) {
	rows, err := r.queries.ListTeamSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list team sizes: %w", err)
	}

	items := make([]TeamSize, 0, len(rows))
	for _, row := range rows {
		var minSize *int
		if row.MinSize != nil {
			value := int(*row.MinSize)
			minSize = &value
		}

		var maxSize *int
		if row.MaxSize != nil {
			value := int(*row.MaxSize)
			maxSize = &value
		}

		items = append(items, TeamSize{
			ID:        row.Uuid,
			Label:     row.Label,
			MinSize:   minSize,
			MaxSize:   maxSize,
			SortOrder: int(row.SortOrder),
		})
	}

	return items, nil
}

// ReferenceVersion returns a version token for cache revalidation.
func (r *Repository) ReferenceVersion(ctx context.Context, table string) (string, error) {
	var (
		value any
		err   error
	)

	switch table {
	case "industries":
		value, err = r.queries.IndustriesReferenceVersion(ctx)
	case "hiring_tools":
		value, err = r.queries.HiringToolsReferenceVersion(ctx)
	case "hiring_frustrations":
		value, err = r.queries.HiringFrustrationsReferenceVersion(ctx)
	case "roles":
		value, err = r.queries.RolesReferenceVersion(ctx)
	case "team_sizes":
		value, err = r.queries.TeamSizesReferenceVersion(ctx)
	default:
		return "", fmt.Errorf("reference version for %s: unknown table", table)
	}

	if err != nil {
		return "", fmt.Errorf("reference version for %s: %w", table, err)
	}

	version, err := db.StringValue(value)
	if err != nil {
		return "", fmt.Errorf("reference version for %s: %w", table, err)
	}

	return version, nil
}

// ResolveIndustryIDs maps public UUIDs to internal IDs and returns the other slug ID.
func (r *Repository) ResolveIndustryIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.queries.ResolveIndustryIDs(ctx, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve industries ids: %w", err)
	}

	return mapResolvedIDs(rows, len(ids), func(row sqlc.ResolveIndustryIDsRow) (uuid.UUID, int64, string) {
		return row.Uuid, row.ID, row.Slug
	})
}

// ResolveHiringToolIDs maps public UUIDs to internal IDs.
func (r *Repository) ResolveHiringToolIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.queries.ResolveHiringToolIDs(ctx, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve hiring_tools ids: %w", err)
	}

	return mapResolvedIDs(rows, len(ids), func(row sqlc.ResolveHiringToolIDsRow) (uuid.UUID, int64, string) {
		return row.Uuid, row.ID, row.Slug
	})
}

// ResolveFrustrationIDs maps public UUIDs to internal IDs.
func (r *Repository) ResolveFrustrationIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.queries.ResolveFrustrationIDs(ctx, ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve hiring_frustrations ids: %w", err)
	}

	return mapResolvedIDs(rows, len(ids), func(row sqlc.ResolveFrustrationIDsRow) (uuid.UUID, int64, string) {
		return row.Uuid, row.ID, row.Slug
	})
}

// ResolveRoleID maps a public UUID to an internal role ID.
func (r *Repository) ResolveRoleID(ctx context.Context, id uuid.UUID) (int64, error) {
	internalID, err := r.queries.ResolveRoleID(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("resolve roles id: %w", err)
	}

	return internalID, nil
}

// ResolveTeamSizeID maps a public UUID to an internal team size ID.
func (r *Repository) ResolveTeamSizeID(ctx context.Context, id uuid.UUID) (int64, error) {
	internalID, err := r.queries.ResolveTeamSizeID(ctx, id)
	if err != nil {
		return 0, fmt.Errorf("resolve team_sizes id: %w", err)
	}

	return internalID, nil
}

// UUIDsForIndustryIDs maps internal industry IDs to public UUIDs.
func (r *Repository) UUIDsForIndustryIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[int64]uuid.UUID{}, nil
	}

	rows, err := r.queries.UUIDsForIndustryIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load industries uuids: %w", err)
	}

	return mapUUIDsForIDs(rows, func(row sqlc.UUIDsForIndustryIDsRow) (int64, uuid.UUID) {
		return row.ID, row.Uuid
	}), nil
}

// UUIDsForHiringToolIDs maps internal hiring tool IDs to public UUIDs.
func (r *Repository) UUIDsForHiringToolIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[int64]uuid.UUID{}, nil
	}

	rows, err := r.queries.UUIDsForHiringToolIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load hiring_tools uuids: %w", err)
	}

	return mapUUIDsForIDs(rows, func(row sqlc.UUIDsForHiringToolIDsRow) (int64, uuid.UUID) {
		return row.ID, row.Uuid
	}), nil
}

// UUIDsForFrustrationIDs maps internal frustration IDs to public UUIDs.
func (r *Repository) UUIDsForFrustrationIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[int64]uuid.UUID{}, nil
	}

	rows, err := r.queries.UUIDsForFrustrationIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load hiring_frustrations uuids: %w", err)
	}

	return mapUUIDsForIDs(rows, func(row sqlc.UUIDsForFrustrationIDsRow) (int64, uuid.UUID) {
		return row.ID, row.Uuid
	}), nil
}

// UUIDForRoleID maps an internal role ID to a public UUID.
func (r *Repository) UUIDForRoleID(ctx context.Context, id int64) (uuid.UUID, error) {
	publicID, err := r.queries.UUIDForRoleID(ctx, id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load roles uuid: %w", err)
	}

	return publicID, nil
}

// UUIDForTeamSizeID maps an internal team size ID to a public UUID.
func (r *Repository) UUIDForTeamSizeID(ctx context.Context, id int64) (uuid.UUID, error) {
	publicID, err := r.queries.UUIDForTeamSizeID(ctx, id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("load team_sizes uuid: %w", err)
	}

	return publicID, nil
}

func mapResolvedIDs[T any](
	rows []T,
	expected int,
	accessor func(T) (uuid.UUID, int64, string),
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	resolved = make(map[uuid.UUID]int64, expected)
	for _, row := range rows {
		publicID, internalID, slug := accessor(row)
		resolved[publicID] = internalID
		if slug == "other" {
			otherID = internalID
		}
	}

	if len(resolved) != expected {
		return nil, 0, fmt.Errorf("unknown reference ids")
	}

	return resolved, otherID, nil
}

func mapUUIDsForIDs[T any](rows []T, accessor func(T) (int64, uuid.UUID)) map[int64]uuid.UUID {
	result := make(map[int64]uuid.UUID, len(rows))
	for _, row := range rows {
		internalID, publicID := accessor(row)
		result[internalID] = publicID
	}

	return result
}

// NowUTC is a test seam for time.
var NowUTC = func() time.Time {
	return time.Now().UTC()
}
