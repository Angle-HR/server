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

// InsertWaitlistSignup returns SQL and args for a regional waitlist signup row.
func InsertWaitlistSignup(
	fullName, email string,
	countryID uuid.UUID,
	region, regionSource string,
	metadata []byte,
) (string, []any, error) {
	return mustSQL(postgres.Insert("waitlist").
		Columns("full_name", "email", "country_id", "region", "region_source", "metadata").
		Values(fullName, email, countryID, region, regionSource, metadata).
		OnConflict("email").DoNothing().
		Returning("uuid").
		ToSQL())
}

// InsertWaitlistRegistry returns SQL and args for a global waitlist.registry row.
func InsertWaitlistRegistry(email, region, regionSource string, waitlistToken uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Insert("registry").
		Columns("email", "region", "region_source", "waitlist_token").
		Values(email, region, regionSource, waitlistToken).
		OnConflict("email").DoNothing().
		ToSQL())
}

// LookupWaitlistRegistryByToken returns SQL to resolve region and email for a waitlist token.
func LookupWaitlistRegistryByToken(token uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("email", "region").
		From("registry").
		Where("waitlist_token", "=", token).
		ToSQL())
}

// LookupWaitlistByUUID returns SQL for a waitlist row by public token.
func LookupWaitlistByUUID(token uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id", "full_name", "email", "onboarding_submitted_at").
		From("waitlist").
		Where("uuid", "=", token).
		WhereNull("deleted_at").
		ToSQL())
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

// SubmitWaitlistOnboarding returns SQL to finalize onboarding on a waitlist row.
func SubmitWaitlistOnboarding(
	waitlistID int64,
	wantsEarlyAccess, wantsUserTesting bool,
	roleID, teamSizeID uuid.UUID,
) (string, []any, error) {
	return mustSQL(postgres.Update("waitlist").
		SetRaw("onboarding_submitted_at", "now()").
		Set("wants_early_access", wantsEarlyAccess).
		Set("wants_user_testing", wantsUserTesting).
		Set("role_id", roleID).
		Set("team_size_id", teamSizeID).
		Where("id", "=", waitlistID).
		WhereNull("onboarding_submitted_at").
		Returning("id").
		ToSQL())
}

func uuidArgs(ids []uuid.UUID) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	return args
}

// InsertWaitlistIndustry returns SQL for a waitlist industry junction row.
func InsertWaitlistIndustry(waitlistID int64, industryID uuid.UUID, otherText *string) (string, []any, error) {
	return mustSQL(postgres.Insert("waitlist_industries").
		Columns("waitlist_id", "industry_id", "other_text").
		Values(waitlistID, industryID, otherText).
		ToSQL())
}

// InsertWaitlistHiringTool returns SQL for a waitlist hiring tool junction row.
func InsertWaitlistHiringTool(waitlistID int64, toolID uuid.UUID, otherText *string) (string, []any, error) {
	return mustSQL(postgres.Insert("waitlist_hiring_tools").
		Columns("waitlist_id", "hiring_tool_id", "other_text").
		Values(waitlistID, toolID, otherText).
		ToSQL())
}

// InsertWaitlistFrustration returns SQL for a waitlist frustration junction row.
func InsertWaitlistFrustration(waitlistID int64, frustrationID uuid.UUID, otherText *string) (string, []any, error) {
	return mustSQL(postgres.Insert("waitlist_frustrations").
		Columns("waitlist_id", "frustration_id", "other_text").
		Values(waitlistID, frustrationID, otherText).
		ToSQL())
}
