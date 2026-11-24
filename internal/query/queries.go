// Package query builds SQL statements with sql-go-query-builder.
package query

import (
	"fmt"

	qb "github.com/Software78/sql-go-query-builder"
	"github.com/Software78/sql-go-query-builder/builder"
	"github.com/google/uuid"
)

var postgres = qb.NewPostgres()

// ListActiveCountries returns SQL and args for active countries ordered for display.
func ListActiveCountries() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "region", "icon_key").
		From("countries").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// LookupCountryByID returns SQL and args for a single active country.
func LookupCountryByID(countryID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "region", "icon_key").
		From("countries").
		Where("id", "=", countryID).
		Where("is_active", "=", true).
		ToSQL())
}

// ListActiveIndustries returns SQL and args for active industries.
func ListActiveIndustries() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "emoji").
		From("industries").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// ListActiveHiringTools returns SQL and args for active hiring tools.
func ListActiveHiringTools() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "icon_url").
		From("hiring_tools").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// ListActiveHiringFrustrations returns SQL and args for active hiring frustrations.
func ListActiveHiringFrustrations() (string, []any, error) {
	return mustSQL(postgres.Select("id", "description", "slug", "emoji").
		From("hiring_frustrations").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		ToSQL())
}

// ListActiveRoles returns SQL and args for active roles.
// Uses schema-qualified raw SQL: the query builder quotes From() as one identifier,
// and global search_path prefers admin.roles (RBAC) over waitlist.roles.
func ListActiveRoles() (string, []any, error) {
	return `SELECT id, name, slug, emoji FROM waitlist.roles WHERE is_active = $1 ORDER BY sort_order ASC, name ASC`,
		[]any{true}, nil
}

// ListTeamSizes returns SQL and args for team size options.
func ListTeamSizes() (string, []any, error) {
	return mustSQL(postgres.Select("id", "label", "min_size", "max_size").
		From("team_sizes").
		OrderBy("sort_order", builder.ASC).
		ToSQL())
}

// ListWaitlistBusinessTypes returns SQL and args for active waitlist business types.
// Uses schema-qualified raw SQL to avoid conflicts with accounts.business_types if search_path is broad.
func ListWaitlistBusinessTypes() (string, []any, error) {
	return `SELECT id, name, slug FROM waitlist.business_types WHERE is_active = $1 ORDER BY sort_order ASC, name ASC`,
		[]any{true}, nil
}

// ActiveIndustriesByIDs returns SQL to load active industries for the given IDs.
func ActiveIndustriesByIDs(ids []uuid.UUID) (string, []any, error) {
	if len(ids) == 0 {
		return "", nil, fmt.Errorf("ids required")
	}

	return mustSQL(postgres.Select("id", "slug").
		From("industries").
		WhereIn("id", uuidArgs(ids)...).
		Where("is_active", "=", true).
		ToSQL())
}

// ActiveHiringToolsByIDs returns SQL to load active hiring tools for the given IDs.
func ActiveHiringToolsByIDs(ids []uuid.UUID) (string, []any, error) {
	if len(ids) == 0 {
		return "", nil, fmt.Errorf("ids required")
	}

	return mustSQL(postgres.Select("id", "slug").
		From("hiring_tools").
		WhereIn("id", uuidArgs(ids)...).
		Where("is_active", "=", true).
		ToSQL())
}

// ActiveHiringFrustrationsByIDs returns SQL to load active frustrations for the given IDs.
func ActiveHiringFrustrationsByIDs(ids []uuid.UUID) (string, []any, error) {
	if len(ids) == 0 {
		return "", nil, fmt.Errorf("ids required")
	}

	return mustSQL(postgres.Select("id", "slug").
		From("hiring_frustrations").
		WhereIn("id", uuidArgs(ids)...).
		Where("is_active", "=", true).
		ToSQL())
}

// ActiveRoleByID returns SQL to load an active role.
// Uses schema-qualified raw SQL: the query builder quotes From() as one identifier,
// and global search_path prefers admin.roles (RBAC) over waitlist.roles.
func ActiveRoleByID(id uuid.UUID) (string, []any, error) {
	return `SELECT id, slug FROM waitlist.roles WHERE id = $1 AND is_active = $2`,
		[]any{id, true}, nil
}

// TeamSizeByID returns SQL to load a team size option.
func TeamSizeByID(id uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id").
		From("team_sizes").
		Where("id", "=", id).
		ToSQL())
}

func uuidArgs(ids []uuid.UUID) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	return args
}
