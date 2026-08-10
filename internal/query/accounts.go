package query

import (
	"fmt"

	"github.com/Software78/sql-go-query-builder/builder"
	"github.com/google/uuid"
)

// ListBusinessTypes returns SQL and args for active business types.
func ListBusinessTypes() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug").
		From("business_types").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// ListOnboardingIndustries returns SQL and args for active onboarding industries.
func ListOnboardingIndustries() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "emoji").
		From("onboarding_industries").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// ListCompanyRoles returns SQL and args for active company roles.
func ListCompanyRoles() (string, []any, error) {
	return mustSQL(postgres.Select("id", "name", "slug", "icon_key").
		From("company_roles").
		Where("is_active", "=", true).
		OrderBy("sort_order", builder.ASC).
		OrderBy("name", builder.ASC).
		ToSQL())
}

// BusinessTypeByID returns SQL to load an active business type.
func BusinessTypeByID(id uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id", "slug").
		From("business_types").
		Where("id", "=", id).
		Where("is_active", "=", true).
		ToSQL())
}

// OnboardingIndustryByID returns SQL to load an active onboarding industry.
func OnboardingIndustryByID(id uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id", "slug").
		From("onboarding_industries").
		Where("id", "=", id).
		Where("is_active", "=", true).
		ToSQL())
}

// CompanyRoleByID returns SQL to load an active company role.
func CompanyRoleByID(id uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("id", "slug").
		From("company_roles").
		Where("id", "=", id).
		Where("is_active", "=", true).
		ToSQL())
}

// InsertAccountUser returns SQL to create an unverified account user.
func InsertAccountUser(email, passwordHash string) (string, []any, error) {
	return mustSQL(postgres.Insert("users").
		Columns("email", "password_hash").
		Values(email, passwordHash).
		OnConflict("email").DoNothing().
		Returning("id").
		ToSQL())
}

// LookupAccountUserByEmail returns SQL to load a user by email.
func LookupAccountUserByEmail(email string) (string, []any, error) {
	return mustSQL(postgres.Select(
		"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at",
		"account_type", "first_name", "last_name", "legal_full_name", "country_id",
	).
		From("users").
		Where("email", "=", email).
		WhereNull("deleted_at").
		ToSQL())
}

// LookupAccountUserByID returns SQL to load a user by ID.
func LookupAccountUserByID(userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select(
		"id", "email", "password_hash", "email_verified_at", "onboarding_completed_at",
		"account_type", "first_name", "last_name", "legal_full_name", "country_id",
	).
		From("users").
		Where("id", "=", userID).
		WhereNull("deleted_at").
		ToSQL())
}

// UpdateAccountUserEmail returns SQL to change email on an unverified user.
func UpdateAccountUserEmail(userID uuid.UUID, email string) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		Set("email", email).
		Where("id", "=", userID).
		WhereNull("email_verified_at").
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}

// SetAccountUserVerified returns SQL to mark a user email verified.
func SetAccountUserVerified(userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		SetRaw("email_verified_at", "now()").
		Where("id", "=", userID).
		WhereNull("email_verified_at").
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}

// UpdateAccountUserIndividualProfile returns SQL to save individual profile fields.
func UpdateAccountUserIndividualProfile(
	userID uuid.UUID,
	firstName, lastName string,
	countryID uuid.UUID,
) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		Set("account_type", "individual").
		Set("first_name", firstName).
		Set("last_name", lastName).
		Set("country_id", countryID).
		Where("id", "=", userID).
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}

// UpdateAccountUserBusinessProfile returns SQL to save business profile fields on the user row.
func UpdateAccountUserBusinessProfile(userID uuid.UUID, legalFullName string) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		Set("account_type", "business").
		Set("legal_full_name", legalFullName).
		Where("id", "=", userID).
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}

// SetAccountOnboardingCompleted returns SQL to finalize onboarding.
func SetAccountOnboardingCompleted(userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		SetRaw("onboarding_completed_at", "now()").
		Where("id", "=", userID).
		WhereNull("onboarding_completed_at").
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}

// UpsertOnboardingProgress returns SQL to insert or update onboarding progress.
func UpsertOnboardingProgress(userID uuid.UUID, currentStep string, completedSteps []string) (string, []any, error) {
	return mustSQL(postgres.Insert("onboarding_progress").
		Columns("user_id", "current_step", "completed_steps").
		Values(userID, currentStep, completedSteps).
		OnConflict("user_id").
		DoUpdate("current_step", currentStep).
		DoUpdate("completed_steps", completedSteps).
		Back().
		ToSQL())
}

// LookupOnboardingProgress returns SQL to load progress for a user.
func LookupOnboardingProgress(userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select("user_id", "current_step", "completed_steps").
		From("onboarding_progress").
		Where("user_id", "=", userID).
		ToSQL())
}

// UpsertOrganizationProfile returns SQL to insert or update organization profile fields.
func UpsertOrganizationProfile(
	ownerUserID uuid.UUID,
	legalName string,
	companyRoleID uuid.UUID,
) (string, []any, error) {
	return mustSQL(postgres.Insert("organizations").
		Columns("owner_user_id", "legal_name", "company_role_id").
		Values(ownerUserID, legalName, companyRoleID).
		OnConflict("owner_user_id").
		DoUpdate("legal_name", legalName).
		DoUpdate("company_role_id", companyRoleID).
		Back().
		Returning("id").
		ToSQL())
}

// UpdateOrganizationBusiness returns SQL to save business compliance fields.
func UpdateOrganizationBusiness(
	ownerUserID uuid.UUID,
	businessTypeID, industryID uuid.UUID,
	employeeCount int,
) (string, []any, error) {
	return mustSQL(postgres.Update("organizations").
		Set("business_type_id", businessTypeID).
		Set("industry_id", industryID).
		Set("employee_count", employeeCount).
		Where("owner_user_id", "=", ownerUserID).
		Returning("id").
		ToSQL())
}

// LookupOrganizationByOwner returns SQL to load organization for a user.
func LookupOrganizationByOwner(ownerUserID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select(
		"id", "legal_name", "company_role_id", "business_type_id", "industry_id", "employee_count",
	).
		From("organizations").
		Where("owner_user_id", "=", ownerUserID).
		ToSQL())
}

// UpsertAccountAddress returns SQL to insert or update a user address.
func UpsertAccountAddress(
	userID uuid.UUID,
	organizationID *uuid.UUID,
	countryID uuid.UUID,
	entryMode, line1 string,
	line2 *string,
	city, stateOrCounty, postCode string,
	formattedAddress *string,
) (string, []any, error) {
	return mustSQL(postgres.Insert("addresses").
		Columns(
			"user_id", "organization_id", "country_id", "entry_mode",
			"line_1", "line_2", "city", "state_or_county", "post_code",
			"formatted_address", "verification_status",
		).
		Values(
			userID, organizationID, countryID, entryMode,
			line1, line2, city, stateOrCounty, postCode,
			formattedAddress, "unverified",
		).
		OnConflict("user_id").
		DoUpdate("organization_id", organizationID).
		DoUpdate("country_id", countryID).
		DoUpdate("entry_mode", entryMode).
		DoUpdate("line_1", line1).
		DoUpdate("line_2", line2).
		DoUpdate("city", city).
		DoUpdate("state_or_county", stateOrCounty).
		DoUpdate("post_code", postCode).
		DoUpdate("formatted_address", formattedAddress).
		DoUpdate("verification_status", "unverified").
		Back().
		Returning("id").
		ToSQL())
}

// LookupAccountAddressByUser returns SQL to load address for a user.
func LookupAccountAddressByUser(userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Select(
		"id", "country_id", "entry_mode", "line_1", "line_2", "city",
		"state_or_county", "post_code", "formatted_address", "verification_status",
	).
		From("addresses").
		Where("user_id", "=", userID).
		ToSQL())
}

// LookupUsersRegistryByEmail returns SQL to resolve registry row by email.
func LookupUsersRegistryByEmail(email string) (string, []any, error) {
	return mustSQL(postgres.Select("id", "email", "region", "user_id").
		From("users_registry").
		Where("email", "=", email).
		ToSQL())
}

// UpsertUsersRegistryProductUser returns SQL to link a product user in the global registry.
func UpsertUsersRegistryProductUser(email, region, regionSource string, userID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Insert("users_registry").
		Columns("email", "region", "region_source", "user_id").
		Values(email, region, regionSource, userID).
		OnConflict("email").
		DoUpdate("region", region).
		DoUpdate("region_source", regionSource).
		DoUpdate("user_id", userID).
		Back().
		ToSQL())
}

// UpdateUsersRegistryRegion returns SQL to update region for a product user.
func UpdateUsersRegistryRegion(email, region, regionSource string) (string, []any, error) {
	return mustSQL(postgres.Update("users_registry").
		Set("region", region).
		Set("region_source", regionSource).
		Where("email", "=", email).
		ToSQL())
}

// LookupVerifiedRegistryEmail returns SQL to check if email has a verified product account globally.
func LookupVerifiedRegistryEmail(email string) (string, []any, error) {
	return mustSQL(postgres.Select("user_id", "region").
		From("users_registry").
		Where("email", "=", email).
		WhereNotNull("user_id").
		ToSQL())
}

// ClearOrganizationBusinessFields clears business step fields when switching account types.
func ClearOrganizationBusinessFields(ownerUserID uuid.UUID) (string, []any, error) {
	return mustSQL(postgres.Update("organizations").
		Set("business_type_id", nil).
		Set("industry_id", nil).
		Set("employee_count", nil).
		Where("owner_user_id", "=", ownerUserID).
		ToSQL())
}

// DeleteOrganizationByOwner removes organization row when switching to individual.
func DeleteOrganizationByOwner(ownerUserID uuid.UUID) (string, []any, error) {
	sql := `DELETE FROM organizations WHERE owner_user_id = $1`
	return sql, []any{ownerUserID}, nil
}

// InsertInitialOnboardingProgress returns SQL to create initial progress after verify.
func InsertInitialOnboardingProgress(userID uuid.UUID) (string, []any, error) {
	steps := []string{"verify_email"}
	return mustSQL(postgres.Insert("onboarding_progress").
		Columns("user_id", "current_step", "completed_steps").
		Values(userID, "profile", steps).
		OnConflict("user_id").DoNothing().
		ToSQL())
}

// ActiveCompanyRoleByID is an alias-style helper for validation.
func ActiveCompanyRoleByID(id uuid.UUID) (string, []any, error) {
	return CompanyRoleByID(id)
}

// ValidateEmployeeCount returns an error if count is out of range.
func ValidateEmployeeCount(count int) error {
	if count < 1 || count > 10000 {
		return fmt.Errorf("employee_count out of range")
	}

	return nil
}

// UpdateIndividualUserBusinessDetails returns SQL to save business type, industry, and
// employee count directly on the accounts.users row for an individual account.
// This has no relation to the organizations table.
func UpdateIndividualUserBusinessDetails(
	userID uuid.UUID,
	businessTypeID, industryID uuid.UUID,
	employeeCount int,
) (string, []any, error) {
	return mustSQL(postgres.Update("users").
		Set("business_type_id", businessTypeID).
		Set("industry_id", industryID).
		Set("employee_count", employeeCount).
		Where("id", "=", userID).
		WhereNull("deleted_at").
		Returning("id").
		ToSQL())
}
