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
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// IndividualOnboardingHandler handles the individual onboarding endpoint.
type IndividualOnboardingHandler struct {
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	validate *validator.Validate
}

// NewIndividualOnboardingHandler returns an individual onboarding handler.
func NewIndividualOnboardingHandler(router *dbrouter.DBRouter, globalDB globalDB) *IndividualOnboardingHandler {
	return &IndividualOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
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
//	@Summary		Submit individual onboarding
//	@Description	Saves first name, last name, country of residence, business type, industry type, and number of employees for an individual account.
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

	// Validate each referenced catalog entry exists.
	if err := h.ensureIndividualOnboardingCatalogRef(ctx, countryID, query.LookupCountryByID, apperror.MsgInvalidCountryID); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureIndividualOnboardingCatalogRef(ctx, businessTypeID, query.BusinessTypeByID, "unknown business_type_id reference"); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureIndividualOnboardingCatalogRef(ctx, industryID, query.OnboardingIndustryByID, "unknown industry_id reference"); err != nil {
		response.Error(w, r, err)
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, tx)

	// Persist individual profile fields on the user row.
	userSQL, userArgs, err := query.UpdateAccountUserIndividualProfile(userID, req.FirstName, req.LastName, countryID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, userSQL, userArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	// Save business type, industry, and employee count on the individual user's row.
	bizSQL, bizArgs, err := query.UpdateIndividualUserBusinessDetails(userID, businessTypeID, industryID, req.NoOfEmployees)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, bizSQL, bizArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	// Advance onboarding progress now that the profile step is complete.
	progressLookupSQL, progressLookupArgs, err := query.LookupOnboardingProgress(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var currentStep string
	var completedSteps []string
	if err := tx.QueryRow(ctx, progressLookupSQL, progressLookupArgs...).Scan(&userID, &currentStep, &completedSteps); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		currentStep, completedSteps = onboarding.InitialProgress()
	}
	completedSteps = onboarding.AdvanceCompleted(completedSteps, onboarding.StepProfile)
	currentStep = onboarding.StepProfile
	progressSQL, progressArgs, err := query.UpsertOnboardingProgress(userID, currentStep, completedSteps)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, progressSQL, progressArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusCreated, IndividualResponse{
		UserID:         userID.String(),
		FirstName:      req.FirstName,
		LastName:       req.LastName,
		CountryID:      req.CountryID,
		BusinessTypeID: req.BusinessTypeID,
		IndustryID:     req.IndustryID,
		NoOfEmployees:  req.NoOfEmployees,
	})
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
