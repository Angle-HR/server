// Package query builds SQL statements with sql-go-query-builder.
package query

import (
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
		Returning("id").
		ToSQL())
}

// InsertUsersRegistry returns SQL and args for a global users_registry row.
func InsertUsersRegistry(email, region, regionSource string) (string, []any, error) {
	return mustSQL(postgres.Insert("users_registry").
		Columns("email", "region", "region_source").
		Values(email, region, regionSource).
		OnConflict("email").DoNothing().
		ToSQL())
}
