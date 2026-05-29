package catalog

import (
	"context"
	"fmt"
	"time"

	qb "github.com/Software78/sql-go-query-builder"
	"github.com/Software78/sql-go-query-builder/builder"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
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
	pool *pgxpool.Pool
	qb   *qb.QB
}

// NewRepository returns a catalog repository backed by PostgreSQL.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool, qb: qb.NewPostgres()}
}

// ListIndustries returns active industries ordered for display.
func (r *Repository) ListIndustries(ctx context.Context) ([]Industry, error) {
	sql, args, err := r.qb.Select(
		"uuid",
		"name",
		"slug",
		"COALESCE(icon_key, '') AS icon_key",
		"sort_order",
	).
		From("industries").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("build list industries: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list industries: %w", err)
	}
	defer rows.Close()

	items := make([]Industry, 0)
	for rows.Next() {
		var item Industry
		var sortOrder int32
		if scanErr := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.IconKey, &sortOrder); scanErr != nil {
			return nil, fmt.Errorf("scan industry: %w", scanErr)
		}
		item.SortOrder = int(sortOrder)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list industries: %w", err)
	}

	return items, nil
}

// ListHiringTools returns active hiring tools ordered for display.
func (r *Repository) ListHiringTools(ctx context.Context) ([]HiringTool, error) {
	sql, args, err := r.qb.Select(
		"uuid",
		"name",
		"slug",
		"COALESCE(icon_key, '') AS icon_key",
		"COALESCE(category, '') AS category",
		"sort_order",
	).
		From("hiring_tools").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("build list hiring tools: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list hiring tools: %w", err)
	}
	defer rows.Close()

	items := make([]HiringTool, 0)
	for rows.Next() {
		var item HiringTool
		var sortOrder int32
		if scanErr := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.IconKey, &item.Category, &sortOrder); scanErr != nil {
			return nil, fmt.Errorf("scan hiring tool: %w", scanErr)
		}
		item.SortOrder = int(sortOrder)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list hiring tools: %w", err)
	}

	return items, nil
}

// ListHiringFrustrations returns active frustrations ordered for display.
//
//nolint:dupl // Same list pattern as ListRoles with different row types.
func (r *Repository) ListHiringFrustrations(ctx context.Context) ([]HiringFrustration, error) {
	sql, args, err := r.qb.Select("uuid", "description", "slug", "sort_order").
		From("hiring_frustrations").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("description", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("build list hiring frustrations: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list hiring frustrations: %w", err)
	}
	defer rows.Close()

	items := make([]HiringFrustration, 0)
	for rows.Next() {
		var item HiringFrustration
		var sortOrder int32
		if scanErr := rows.Scan(&item.ID, &item.Description, &item.Slug, &sortOrder); scanErr != nil {
			return nil, fmt.Errorf("scan hiring frustration: %w", scanErr)
		}
		item.SortOrder = int(sortOrder)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list hiring frustrations: %w", err)
	}

	return items, nil
}

// ListRoles returns active roles ordered for display.
//
//nolint:dupl // Same list pattern as ListHiringFrustrations with different row types.
func (r *Repository) ListRoles(ctx context.Context) ([]Role, error) {
	sql, args, err := r.qb.Select("uuid", "name", "slug", "sort_order").
		From("roles").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("build list roles: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()

	items := make([]Role, 0)
	for rows.Next() {
		var item Role
		var sortOrder int32
		if scanErr := rows.Scan(&item.ID, &item.Name, &item.Slug, &sortOrder); scanErr != nil {
			return nil, fmt.Errorf("scan role: %w", scanErr)
		}
		item.SortOrder = int(sortOrder)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}

	return items, nil
}

// ListTeamSizes returns team size bands ordered for display.
func (r *Repository) ListTeamSizes(ctx context.Context) ([]TeamSize, error) {
	sql, args, err := r.qb.Select("uuid", "label", "min_size", "max_size", "sort_order").
		From("team_sizes").
		OrderBy("sort_order", builder.ASC).
		OrderBy("label", builder.ASC).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("build list team sizes: %w", err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list team sizes: %w", err)
	}
	defer rows.Close()

	items := make([]TeamSize, 0)
	for rows.Next() {
		var item TeamSize
		var sortOrder int32
		var minSize, maxSize *int32
		if scanErr := rows.Scan(&item.ID, &item.Label, &minSize, &maxSize, &sortOrder); scanErr != nil {
			return nil, fmt.Errorf("scan team size: %w", scanErr)
		}
		if minSize != nil {
			value := int(*minSize)
			item.MinSize = &value
		}
		if maxSize != nil {
			value := int(*maxSize)
			item.MaxSize = &value
		}
		item.SortOrder = int(sortOrder)
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list team sizes: %w", err)
	}

	return items, nil
}

// ReferenceVersion returns a version token for cache revalidation.
func (r *Repository) ReferenceVersion(ctx context.Context, table string) (string, error) {
	switch table {
	case "industries", "hiring_tools", "hiring_frustrations", "roles", "team_sizes":
	default:
		return "", fmt.Errorf("reference version for %s: unknown table", table)
	}

	sql, args, err := r.qb.Select(
		`COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version`,
	).From(table).ToSQL()
	if err != nil {
		return "", fmt.Errorf("reference version for %s: %w", table, err)
	}

	var version string
	if queryErr := r.pool.QueryRow(ctx, sql, args...).Scan(&version); queryErr != nil {
		return "", fmt.Errorf("reference version for %s: %w", table, queryErr)
	}

	return version, nil
}

type resolvedIDRow struct {
	id   int64
	uuid uuid.UUID
	slug string
}

// ResolveIndustryIDs maps public UUIDs to internal IDs and returns the other slug ID.
func (r *Repository) ResolveIndustryIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.resolveIDs(ctx, "industries", ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve industries ids: %w", err)
	}

	return mapResolvedRows(rows, len(ids))
}

// ResolveHiringToolIDs maps public UUIDs to internal IDs.
func (r *Repository) ResolveHiringToolIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.resolveIDs(ctx, "hiring_tools", ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve hiring_tools ids: %w", err)
	}

	return mapResolvedRows(rows, len(ids))
}

// ResolveFrustrationIDs maps public UUIDs to internal IDs.
func (r *Repository) ResolveFrustrationIDs(
	ctx context.Context,
	ids []uuid.UUID,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	if len(ids) == 0 {
		return map[uuid.UUID]int64{}, 0, nil
	}

	rows, err := r.resolveIDs(ctx, "hiring_frustrations", ids)
	if err != nil {
		return nil, 0, fmt.Errorf("resolve hiring_frustrations ids: %w", err)
	}

	return mapResolvedRows(rows, len(ids))
}

func (r *Repository) resolveIDs(ctx context.Context, table string, ids []uuid.UUID) ([]resolvedIDRow, error) {
	sql, args, err := r.qb.Select("id", "uuid", "slug").
		From(table).
		WhereRaw("uuid = ANY(?::uuid[])", ids).
		Where("is_active", "=", true).
		ToSQL()
	if err != nil {
		return nil, err
	}

	pgRows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer pgRows.Close()

	rows := make([]resolvedIDRow, 0, len(ids))
	for pgRows.Next() {
		var row resolvedIDRow
		if scanErr := pgRows.Scan(&row.id, &row.uuid, &row.slug); scanErr != nil {
			return nil, scanErr
		}
		rows = append(rows, row)
	}

	return rows, pgRows.Err()
}

// ResolveRoleID maps a public UUID to an internal role ID.
func (r *Repository) ResolveRoleID(ctx context.Context, id uuid.UUID) (int64, error) {
	sql, args, err := r.qb.Select("id").
		From("roles").
		Where("uuid", "=", id).
		Where("is_active", "=", true).
		ToSQL()
	if err != nil {
		return 0, fmt.Errorf("resolve roles id: %w", err)
	}

	var internalID int64
	if queryErr := r.pool.QueryRow(ctx, sql, args...).Scan(&internalID); queryErr != nil {
		return 0, fmt.Errorf("resolve roles id: %w", queryErr)
	}

	return internalID, nil
}

// ResolveTeamSizeID maps a public UUID to an internal team size ID.
func (r *Repository) ResolveTeamSizeID(ctx context.Context, id uuid.UUID) (int64, error) {
	sql, args, err := r.qb.Select("id").
		From("team_sizes").
		Where("uuid", "=", id).
		ToSQL()
	if err != nil {
		return 0, fmt.Errorf("resolve team_sizes id: %w", err)
	}

	var internalID int64
	if queryErr := r.pool.QueryRow(ctx, sql, args...).Scan(&internalID); queryErr != nil {
		return 0, fmt.Errorf("resolve team_sizes id: %w", queryErr)
	}

	return internalID, nil
}

// UUIDsForIndustryIDs maps internal industry IDs to public UUIDs.
func (r *Repository) UUIDsForIndustryIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	return r.uuidsForIDs(ctx, "industries", ids, "load industries uuids")
}

// UUIDsForHiringToolIDs maps internal hiring tool IDs to public UUIDs.
func (r *Repository) UUIDsForHiringToolIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	return r.uuidsForIDs(ctx, "hiring_tools", ids, "load hiring_tools uuids")
}

// UUIDsForFrustrationIDs maps internal frustration IDs to public UUIDs.
func (r *Repository) UUIDsForFrustrationIDs(ctx context.Context, ids []int64) (map[int64]uuid.UUID, error) {
	return r.uuidsForIDs(ctx, "hiring_frustrations", ids, "load hiring_frustrations uuids")
}

func (r *Repository) uuidsForIDs(
	ctx context.Context,
	table string,
	ids []int64,
	errLabel string,
) (map[int64]uuid.UUID, error) {
	if len(ids) == 0 {
		return map[int64]uuid.UUID{}, nil
	}

	sql, args, err := r.qb.Select("id", "uuid").
		From(table).
		WhereRaw("id = ANY(?::bigint[])", ids).
		ToSQL()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}
	defer rows.Close()

	result := make(map[int64]uuid.UUID, len(ids))
	for rows.Next() {
		var internalID int64
		var publicID uuid.UUID
		if scanErr := rows.Scan(&internalID, &publicID); scanErr != nil {
			return nil, fmt.Errorf("%s: %w", errLabel, scanErr)
		}
		result[internalID] = publicID
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", errLabel, err)
	}

	return result, nil
}

// UUIDForRoleID maps an internal role ID to a public UUID.
func (r *Repository) UUIDForRoleID(ctx context.Context, id int64) (uuid.UUID, error) {
	return r.uuidForID(ctx, "roles", id, "load roles uuid")
}

// UUIDForTeamSizeID maps an internal team size ID to a public UUID.
func (r *Repository) UUIDForTeamSizeID(ctx context.Context, id int64) (uuid.UUID, error) {
	return r.uuidForID(ctx, "team_sizes", id, "load team_sizes uuid")
}

func (r *Repository) uuidForID(ctx context.Context, table string, id int64, errLabel string) (uuid.UUID, error) {
	sql, args, err := r.qb.Select("uuid").
		From(table).
		Where("id", "=", id).
		ToSQL()
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s: %w", errLabel, err)
	}

	var publicID uuid.UUID
	if queryErr := r.pool.QueryRow(ctx, sql, args...).Scan(&publicID); queryErr != nil {
		return uuid.Nil, fmt.Errorf("%s: %w", errLabel, queryErr)
	}

	return publicID, nil
}

func mapResolvedRows(
	rows []resolvedIDRow,
	expected int,
) (resolved map[uuid.UUID]int64, otherID int64, err error) {
	resolved = make(map[uuid.UUID]int64, expected)
	for _, row := range rows {
		resolved[row.uuid] = row.id
		if row.slug == "other" {
			otherID = row.id
		}
	}

	if len(resolved) != expected {
		return nil, 0, fmt.Errorf("unknown reference ids")
	}

	return resolved, otherID, nil
}

// NowUTC is a test seam for time.
var NowUTC = func() time.Time {
	return time.Now().UTC()
}
