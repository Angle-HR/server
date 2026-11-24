package identityhttp

import (
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
)

// INTERIM: these region-aware query pickers are copies of the helpers in internal/handler/region_pending.go (which
// stays with the onboarding half). They go away when users move out of the shared accounts tables.

// dataPool is the minimal connection interface shared by dbrouter.PgxPool (a
// real regional pool) and globalDB (the global database connection). It lets
// resolvePool hand back either one to callers that only Begin/Exec/Query/
// QueryRow, without them needing to know which physical database they got.
type dataPool = globalDB

// resolvePool returns the datastore for reg: the global connection when reg
// is the pending "global" holding region, otherwise the region's dedicated
// regional pool. Every place that used to call router.DB(reg) directly should
// go through this instead, now that "global" accounts exist.
func resolvePool(router *dbrouter.DBRouter, global globalDB, reg region.Region) (dataPool, error) {
	if reg == region.RegionGlobal {
		return global, nil
	}
	return router.DB(reg)
}

// The functions below pick between the regional and pending-holding-area
// query for the same operation, based on reg. They exist so the bulk of each
// handler (auth_handlers.go, product_onboarding_handlers.go, and the legacy
// one-shot onboarding handlers) can stay identical regardless of whether the
// account has a real region yet.

func lookupUserByIDSQL(reg region.Region, userID uuid.UUID) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.LookupPendingUserByID(userID)
	}
	return query.LookupAccountUserByID(userID)
}

func lookupUserByEmailSQL(reg region.Region, email string) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.LookupPendingUserByEmail(email)
	}
	return query.LookupAccountUserByEmail(email)
}

func updateUserEmailSQL(reg region.Region, userID uuid.UUID, email string) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.UpdatePendingUserEmail(userID, email)
	}
	return query.UpdateAccountUserEmail(userID, email)
}

func setUserVerifiedSQL(reg region.Region, userID uuid.UUID) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.SetPendingUserVerified(userID)
	}
	return query.SetAccountUserVerified(userID)
}

func upsertOnboardingProgressSQL(
	reg region.Region,
	userID uuid.UUID,
	step string,
	completed []string,
) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.UpsertPendingOnboardingProgress(userID, step, completed)
	}
	return query.UpsertOnboardingProgress(userID, step, completed)
}

func lookupOnboardingProgressSQL(reg region.Region, userID uuid.UUID) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.LookupPendingOnboardingProgress(userID)
	}
	return query.LookupOnboardingProgress(userID)
}

func updateBusinessProfileSQL(reg region.Region, userID uuid.UUID, legalFullName string) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.UpdatePendingUserBusinessProfile(userID, legalFullName)
	}
	return query.UpdateAccountUserBusinessProfile(userID, legalFullName)
}

func upsertOrganizationProfileSQL(
	reg region.Region,
	userID uuid.UUID,
	legalName string,
	companyRoleID uuid.UUID,
) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.UpsertPendingOrganizationProfile(userID, legalName, companyRoleID)
	}
	return query.UpsertOrganizationProfile(userID, legalName, companyRoleID)
}
