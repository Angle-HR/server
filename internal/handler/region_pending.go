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
func migrateUserToRegion(
	ctx context.Context,
	router *dbrouter.DBRouter,
	global globalDB,
	userID uuid.UUID,
	target region.Region,
) (region.Region, error) {
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
		return checkAlreadyMigrated(ctx, global, userID, target)
	}

	org, err := loadPendingOrganization(ctx, global, userID, &pending)
	if err != nil {
		return region.RegionUnknown, err
	}
	currentStep, completedSteps, err := loadPendingProgress(ctx, global, userID)
	if err != nil {
		return region.RegionUnknown, err
	}

	if copyErr := copyAccountToRegion(ctx, targetPool, &pending, org, currentStep, completedSteps); copyErr != nil {
		return region.RegionUnknown, copyErr
	}

	// From here on the account authoritatively lives in the target region.
	// Everything below is global-DB bookkeeping; if it fails partway, a
	// retry of this whole function is safe (see the ON CONFLICT DO NOTHING
	// inserts above and the registry-trusting ErrNoRows branch at the top).
	if releaseErr := releasePendingAccount(ctx, global, &pending, org != nil, target); releaseErr != nil {
		return region.RegionUnknown, releaseErr
	}
	return target, nil
}

// checkAlreadyMigrated handles a migration retry that finds no pending row: either
// an earlier attempt already finished, or something else moved the account. Trust
// the registry rather than guessing at the outcome.
func checkAlreadyMigrated(
	ctx context.Context,
	global globalDB,
	userID uuid.UUID,
	target region.Region,
) (region.Region, error) {
	const sql = `SELECT region FROM users_registry WHERE user_id = $1`
	var existingReg string
	if scanErr := global.QueryRow(ctx, sql, userID).Scan(&existingReg); scanErr != nil {
		return region.RegionUnknown, scanErr
	}
	if region.Region(existingReg) != target {
		return region.RegionUnknown, fmt.Errorf(
			"region: user %s already migrated to %q, not %q", userID, existingReg, target)
	}
	return target, nil
}

// loadPendingOrganization returns the business's pending organization draft, or nil
// when the account is not a business or has no draft yet (for example when migrating
// straight from a legacy one-shot onboarding endpoint).
func loadPendingOrganization(
	ctx context.Context,
	global globalDB,
	userID uuid.UUID,
	pending *accountUser,
) (*pendingOrganization, error) {
	if pending.AccountType == nil || *pending.AccountType != onboarding.AccountBusiness {
		return nil, nil //nolint:nilnil // nil organization means "nothing to carry over"
	}
	sql, args, err := query.LookupPendingOrganizationByOwner(userID)
	if err != nil {
		return nil, err
	}
	var o pendingOrganization
	err = global.QueryRow(ctx, sql, args...).Scan(&o.ID, &o.LegalName, &o.CompanyRoleID)
	switch {
	case err == nil:
		return &o, nil
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil //nolint:nilnil // no profile-step draft yet: nothing to carry over
	default:
		return nil, err
	}
}

// loadPendingProgress returns the pending onboarding progress, or the initial
// progress when none is stored.
func loadPendingProgress(
	ctx context.Context,
	global globalDB,
	userID uuid.UUID,
) (currentStep string, completedSteps []string, err error) {
	currentStep, completedSteps = onboarding.InitialProgress()
	sql, args, err := query.LookupPendingOnboardingProgress(userID)
	if err != nil {
		return "", nil, err
	}
	var progressUserID uuid.UUID
	if scanErr := global.QueryRow(ctx, sql, args...).
		Scan(&progressUserID, &currentStep, &completedSteps); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return "", nil, scanErr
		}
		currentStep, completedSteps = onboarding.InitialProgress()
	}
	return currentStep, completedSteps, nil
}

// copyAccountToRegion writes the user, organization draft and progress into the
// target region in one transaction.
func copyAccountToRegion(
	ctx context.Context,
	targetPool dataPool,
	pending *accountUser,
	org *pendingOrganization,
	currentStep string,
	completedSteps []string,
) error {
	tx, err := targetPool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnError(ctx, tx)

	userSQL, userArgs, err := query.InsertMigratedAccountUser(
		pending.ID, pending.Email, pending.PasswordHash, pending.EmailVerifiedAt,
		pending.AccountType, pending.LegalFullName,
	)
	if err != nil {
		return err
	}
	if _, execErr := tx.Exec(ctx, userSQL, userArgs...); execErr != nil {
		return execErr
	}

	if org != nil {
		sql, args, buildErr := query.InsertMigratedOrganization(pending.ID, org.LegalName, org.CompanyRoleID)
		if buildErr != nil {
			return buildErr
		}
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			return execErr
		}
	}

	upsertSQL, upsertArgs, err := query.UpsertOnboardingProgress(pending.ID, currentStep, completedSteps)
	if err != nil {
		return err
	}
	if _, execErr := tx.Exec(ctx, upsertSQL, upsertArgs...); execErr != nil {
		return execErr
	}
	return tx.Commit(ctx)
}

// releasePendingAccount points the registry at the new region and deletes the
// pending rows in the global database in one transaction.
func releasePendingAccount(
	ctx context.Context,
	global globalDB,
	pending *accountUser,
	hadOrganization bool,
	target region.Region,
) error {
	gtx, err := global.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackOnError(ctx, gtx)

	regSQL, regArgs, err := query.UpdateUsersRegistryRegion(pending.Email, string(target), regionSourceExplicit)
	if err != nil {
		return err
	}
	if _, execErr := gtx.Exec(ctx, regSQL, regArgs...); execErr != nil {
		return execErr
	}

	type deletion struct {
		sql  string
		args []any
	}
	var deletions []deletion
	if hadOrganization {
		sql, args, buildErr := query.DeletePendingOrganization(pending.ID)
		if buildErr != nil {
			return buildErr
		}
		deletions = append(deletions, deletion{sql, args})
	}
	progressSQL, progressArgs, err := query.DeletePendingOnboardingProgress(pending.ID)
	if err != nil {
		return err
	}
	userSQL, userArgs, err := query.DeletePendingUser(pending.ID)
	if err != nil {
		return err
	}
	deletions = append(deletions, deletion{progressSQL, progressArgs}, deletion{userSQL, userArgs})

	for _, d := range deletions {
		if _, execErr := gtx.Exec(ctx, d.sql, d.args...); execErr != nil {
			return execErr
		}
	}
	return gtx.Commit(ctx)
}
