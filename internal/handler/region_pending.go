package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
)

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

func upsertOnboardingProgressSQL(reg region.Region, userID uuid.UUID, step string, completed []string) (string, []any, error) {
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

func upsertOrganizationProfileSQL(reg region.Region, userID uuid.UUID, legalName string, companyRoleID uuid.UUID) (string, []any, error) {
	if reg == region.RegionGlobal {
		return query.UpsertPendingOrganizationProfile(userID, legalName, companyRoleID)
	}
	return query.UpsertOrganizationProfile(userID, legalName, companyRoleID)
}

type pendingOrganization struct {
	ID            uuid.UUID
	LegalName     string
	CompanyRoleID uuid.UUID
}

// migrateUserToRegion moves an account out of the global holding area
// (accounts.pending_users / pending_organizations / pending_onboarding_progress
// in the global database) into target's real regional database, preserving
// its user id, and repoints the global users_registry at the new region.
//
// It commits the target-region write before touching global state, and every
// insert into the target region is ON CONFLICT DO NOTHING, so it's safe to
// call again if a previous attempt got the account written into its new
// region but failed before the global side (registry update + pending-row
// cleanup) committed.
//
// Callers must reissue the caller's JWTs after this returns — the region on
// their existing tokens is now stale.
func migrateUserToRegion(ctx context.Context, router *dbrouter.DBRouter, global globalDB, userID uuid.UUID, target region.Region) (region.Region, error) {
	if !region.Valid(target) || target == region.RegionGlobal {
		return region.RegionUnknown, fmt.Errorf("region: invalid migration target %q", target)
	}

	targetPool, err := router.DB(target)
	if err != nil {
		return region.RegionUnknown, err
	}

	pendingSQL, pendingArgs, err := query.LookupPendingUserByID(userID)
	if err != nil {
		return region.RegionUnknown, err
	}
	pending, err := scanAccountUser(global.QueryRow(ctx, pendingSQL, pendingArgs...))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return region.RegionUnknown, err
		}

		// No pending row left: either this call is a retry after an earlier
		// attempt already finished, or something else moved it. Trust the
		// registry rather than guessing at the outcome.
		const sql = `SELECT region FROM users_registry WHERE user_id = $1`
		var existingReg string
		if err := global.QueryRow(ctx, sql, userID).Scan(&existingReg); err != nil {
			return region.RegionUnknown, err
		}
		if region.Region(existingReg) != target {
			return region.RegionUnknown, fmt.Errorf(
				"region: user %s already migrated to %q, not %q", userID, existingReg, target)
		}
		return target, nil
	}

	var org *pendingOrganization
	if pending.AccountType != nil && *pending.AccountType == onboarding.AccountBusiness {
		orgSQL, orgArgs, err := query.LookupPendingOrganizationByOwner(userID)
		if err != nil {
			return region.RegionUnknown, err
		}
		var o pendingOrganization
		err = global.QueryRow(ctx, orgSQL, orgArgs...).Scan(&o.ID, &o.LegalName, &o.CompanyRoleID)
		switch {
		case err == nil:
			org = &o
		case errors.Is(err, pgx.ErrNoRows):
			// No profile-step draft yet (e.g. migrating straight from one of
			// the legacy one-shot onboarding endpoints) — nothing to carry over.
		default:
			return region.RegionUnknown, err
		}
	}

	currentStep, completedSteps := onboarding.InitialProgress()
	progressSQL, progressArgs, err := query.LookupPendingOnboardingProgress(userID)
	if err != nil {
		return region.RegionUnknown, err
	}
	var progressUserID uuid.UUID
	if err := global.QueryRow(ctx, progressSQL, progressArgs...).Scan(&progressUserID, &currentStep, &completedSteps); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return region.RegionUnknown, err
		}
		currentStep, completedSteps = onboarding.InitialProgress()
	}

	tx, err := targetPool.Begin(ctx)
	if err != nil {
		return region.RegionUnknown, err
	}
	defer rollbackOnError(ctx, tx)

	userSQL, userArgs, err := query.InsertMigratedAccountUser(
		pending.ID, pending.Email, pending.PasswordHash, pending.EmailVerifiedAt,
		pending.AccountType, pending.LegalFullName,
	)
	if err != nil {
		return region.RegionUnknown, err
	}
	if _, err := tx.Exec(ctx, userSQL, userArgs...); err != nil {
		return region.RegionUnknown, err
	}

	if org != nil {
		orgInsertSQL, orgInsertArgs, err := query.InsertMigratedOrganization(userID, org.LegalName, org.CompanyRoleID)
		if err != nil {
			return region.RegionUnknown, err
		}
		if _, err := tx.Exec(ctx, orgInsertSQL, orgInsertArgs...); err != nil {
			return region.RegionUnknown, err
		}
	}

	upsertSQL, upsertArgs, err := query.UpsertOnboardingProgress(userID, currentStep, completedSteps)
	if err != nil {
		return region.RegionUnknown, err
	}
	if _, err := tx.Exec(ctx, upsertSQL, upsertArgs...); err != nil {
		return region.RegionUnknown, err
	}

	if err := tx.Commit(ctx); err != nil {
		return region.RegionUnknown, err
	}

	// From here on the account authoritatively lives in the target region.
	// Everything below is global-DB bookkeeping; if it fails partway, a
	// retry of this whole function is safe (see the ON CONFLICT DO NOTHING
	// inserts above and the registry-trusting ErrNoRows branch at the top).
	gtx, err := global.Begin(ctx)
	if err != nil {
		return region.RegionUnknown, err
	}
	defer rollbackOnError(ctx, gtx)

	regSQL, regArgs, err := query.UpdateUsersRegistryRegion(pending.Email, string(target), regionSourceExplicit)
	if err != nil {
		return region.RegionUnknown, err
	}
	if _, err := gtx.Exec(ctx, regSQL, regArgs...); err != nil {
		return region.RegionUnknown, err
	}

	if org != nil {
		delOrgSQL, delOrgArgs, err := query.DeletePendingOrganization(userID)
		if err != nil {
			return region.RegionUnknown, err
		}
		if _, err := gtx.Exec(ctx, delOrgSQL, delOrgArgs...); err != nil {
			return region.RegionUnknown, err
		}
	}

	delProgressSQL, delProgressArgs, err := query.DeletePendingOnboardingProgress(userID)
	if err != nil {
		return region.RegionUnknown, err
	}
	if _, err := gtx.Exec(ctx, delProgressSQL, delProgressArgs...); err != nil {
		return region.RegionUnknown, err
	}

	delUserSQL, delUserArgs, err := query.DeletePendingUser(userID)
	if err != nil {
		return region.RegionUnknown, err
	}
	if _, err := gtx.Exec(ctx, delUserSQL, delUserArgs...); err != nil {
		return region.RegionUnknown, err
	}

	if err := gtx.Commit(ctx); err != nil {
		return region.RegionUnknown, err
	}

	return target, nil
}
