package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// IndividualOnboardingHandler handles the individual onboarding endpoint.
type IndividualOnboardingHandler struct {
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	Tokens   *auth.TokenService
	validate *validator.Validate
}

// NewIndividualOnboardingHandler returns an individual onboarding handler.
// Tokens is used to reissue JWTs when this submission migrates the account
// out of the global holding region (see NewProductOnboardingHandler).
func NewIndividualOnboardingHandler(
	router *dbrouter.DBRouter,
	globalDB globalDB,
	tokens *auth.TokenService,
) *IndividualOnboardingHandler {
	return &IndividualOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Tokens:   tokens,
		validate: validator.New(),
	}
}

// RegisterProtectedRoutes mounts the authenticated individual onboarding route.
func (h *IndividualOnboardingHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Post("/onboarding/individual", h.submitIndividualOnboarding)
}

type individualOnboardingRequest struct {
	FirstName      string `json:"first_name"       validate:"required,max=120"`
	LastName       string `json:"last_name"        validate:"required,max=120"`
	CountryID      string `json:"country_id"       validate:"required,uuid"`
	BusinessTypeID string `json:"business_type_id" validate:"required,uuid"`
	IndustryID     string `json:"industry_id"      validate:"required,uuid"`
	NoOfEmployees  int    `json:"no_of_employees"  validate:"required,min=1,max=10000"`
}

func (r *individualOnboardingRequest) trim() {
	r.FirstName = strings.TrimSpace(r.FirstName)
	r.LastName = strings.TrimSpace(r.LastName)
	r.CountryID = strings.TrimSpace(r.CountryID)
	r.BusinessTypeID = strings.TrimSpace(r.BusinessTypeID)
	r.IndustryID = strings.TrimSpace(r.IndustryID)
}

// submitIndividualOnboarding godoc
//
//	@Summary		Submit individual onboarding (deprecated)
//	@Description	Deprecated: prefer the stepped flow PUT /onboarding/profile then address/complete. Saves first name, last name, country, business type, industry, and employee count on the user row only.
//	@Deprecated
//	@Tags			onboarding/individual
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.IndividualRequest	true	"Individual onboarding payload"
//	@Success		201		{object}	handler.IndividualEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/individual [post]
func (h *IndividualOnboardingHandler) submitIndividualOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	slog.Info("individual onboarding auth check", "user_id", userID, "region", reg, "ok", ok)
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	var req individualOnboardingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}

	req.trim()

	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()

	// Parse UUIDs (validation above guarantees they parse cleanly).
	countryID := uuid.MustParse(req.CountryID)
	businessTypeID := uuid.MustParse(req.BusinessTypeID)
	industryID := uuid.MustParse(req.IndustryID)

	if err := h.ensureIndividualRefsExist(ctx, countryID, businessTypeID, industryID); err != nil {
		response.Error(w, r, err)
		return
	}

	// This one-shot submission includes the country directly, so a still-
	// global account migrates into its real region right here.
	originalReg := reg
	if reg == region.RegionGlobal {
		newReg, err := h.migrateForCountry(ctx, userID, countryID)
		if err != nil {
			response.Error(w, r, err)
			return
		}
		reg = newReg
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err = persistIndividualOnboarding(ctx, pool, userID, &req, countryID, businessTypeID, industryID); err != nil {
		response.Error(w, r, err)
		return
	}

	var tokens *RegionReissue
	if originalReg != reg {
		issued, err := h.Tokens.IssuePair(userID, reg, true)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		tokens = &RegionReissue{
			AccessToken:  issued.AccessToken,
			RefreshToken: issued.RefreshToken,
			ExpiresIn:    issued.ExpiresIn,
		}
	}

	response.Success(w, r, http.StatusCreated, IndividualResponse{
		UserID:         userID.String(),
		FirstName:      req.FirstName,
		LastName:       req.LastName,
		CountryID:      req.CountryID,
		BusinessTypeID: req.BusinessTypeID,
		IndustryID:     req.IndustryID,
		NoOfEmployees:  req.NoOfEmployees,
		Tokens:         tokens,
	})
}

// ensureIndividualRefsExist checks that every referenced catalog entry exists.
func (h *IndividualOnboardingHandler) ensureIndividualRefsExist(
	ctx context.Context,
	countryID, businessTypeID, industryID uuid.UUID,
) error {
	refs := []struct {
		id    uuid.UUID
		build func(uuid.UUID) (string, []any, error)
		msg   string
	}{
		{countryID, query.LookupCountryByID, apperror.MsgInvalidCountryID},
		{businessTypeID, query.BusinessTypeByID, "unknown business_type_id reference"},
		{industryID, query.OnboardingIndustryByID, "unknown industry_id reference"},
	}
	for _, ref := range refs {
		if err := h.ensureIndividualOnboardingCatalogRef(ctx, ref.id, ref.build, ref.msg); err != nil {
			return err
		}
	}
	return nil
}

// migrateForCountry moves a still-global account into the region of the given country.
func (h *IndividualOnboardingHandler) migrateForCountry(
	ctx context.Context,
	userID, countryID uuid.UUID,
) (region.Region, error) {
	countrySQL, countryArgs, err := query.LookupCountryByID(countryID)
	if err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	var target string
	if scanErr := h.GlobalDB.QueryRow(ctx, countrySQL, countryArgs...).
		Scan(new(uuid.UUID), new(string), new(string), &target, new(*string)); scanErr != nil {
		return region.RegionUnknown, apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidCountryID)
	}
	newReg, err := migrateUserToRegion(ctx, h.Router, h.GlobalDB, userID, region.Region(target))
	if err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	return newReg, nil
}

// persistIndividualOnboarding writes the individual's profile and business details
// and marks the profile step complete, in one transaction.
func persistIndividualOnboarding(
	ctx context.Context,
	pool dataPool,
	userID uuid.UUID,
	req *individualOnboardingRequest,
	countryID, businessTypeID, industryID uuid.UUID,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	// Persist individual profile fields on the user row.
	userSQL, userArgs, err := query.UpdateAccountUserIndividualProfile(userID, req.FirstName, req.LastName, countryID)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, userSQL, userArgs...); execErr != nil {
		return apperror.ErrInternal
	}

	// Save business type, industry, and employee count on the individual user's row.
	bizSQL, bizArgs, err := query.UpdateIndividualUserBusinessDetails(
		userID, businessTypeID, industryID, req.NoOfEmployees,
	)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, bizSQL, bizArgs...); execErr != nil {
		return apperror.ErrInternal
	}

	if err = advanceProfileProgress(ctx, tx, userID); err != nil {
		return apperror.ErrInternal
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apperror.ErrInternal
	}
	return nil
}

// advanceProfileProgress marks the profile step complete for the user.
func advanceProfileProgress(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	lookupSQL, lookupArgs, err := query.LookupOnboardingProgress(userID)
	if err != nil {
		return err
	}
	var currentStep string
	var completedSteps []string
	if scanErr := tx.QueryRow(ctx, lookupSQL, lookupArgs...).
		Scan(new(uuid.UUID), &currentStep, &completedSteps); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return scanErr
		}
		_, completedSteps = onboarding.InitialProgress()
	}
	completedSteps = onboarding.AdvanceCompleted(completedSteps, onboarding.StepProfile)
	upsertSQL, upsertArgs, err := query.UpsertOnboardingProgress(userID, onboarding.StepProfile, completedSteps)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, upsertSQL, upsertArgs...)
	return err
}

// ensureIndividualOnboardingCatalogRef validates that a UUID resolves to an active catalog row.
// build must be a query builder that selects at least (id, slug).
func (h *IndividualOnboardingHandler) ensureIndividualOnboardingCatalogRef(
	ctx context.Context,
	id uuid.UUID,
	build func(uuid.UUID) (string, []any, error),
	missingMsg string,
) error {
	sql, args, err := build(id)
	if err != nil {
		return fmt.Errorf("build catalog ref query: %w", err)
	}
	rows, err := h.GlobalDB.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("catalog ref lookup: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return apperror.New(apperror.CodeInvalidReference, missingMsg)
	}
	return nil
}
