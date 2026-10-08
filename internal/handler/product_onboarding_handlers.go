package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/mailer"
	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/queue"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// ProductOnboardingHandler handles product onboarding endpoints.
type ProductOnboardingHandler struct {
	Router          *dbrouter.DBRouter
	GlobalDB        globalDB
	Enqueuer        jobEnqueuer
	Tokens          *auth.TokenService
	AddressProvider onboarding.AddressVerifier
	AddressSearcher onboarding.AddressSearcher
	validate        *validator.Validate
}

// NewProductOnboardingHandler returns a product onboarding handler. Tokens is
// used to reissue JWTs when a step migrates the account out of the global
// holding region into a real one.
func NewProductOnboardingHandler(
	router *dbrouter.DBRouter,
	globalDB globalDB,
	enqueuer jobEnqueuer,
	tokens *auth.TokenService,
) *ProductOnboardingHandler {
	return &ProductOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Enqueuer: enqueuer,
		Tokens:   tokens,
		validate: validator.New(),
	}
}

// reissueIfMigrated returns fresh JWTs when reg differs from the account's
// original region (i.e. this request just migrated it out of the global
// holding area), or nil when no migration happened.
func (h *ProductOnboardingHandler) reissueIfMigrated(
	userID uuid.UUID,
	originalReg, reg region.Region,
) (*RegionReissue, error) {
	if originalReg == reg {
		return nil, nil
	}
	tokens, err := h.Tokens.IssuePair(userID, reg, true)
	if err != nil {
		return nil, err
	}
	return &RegionReissue{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		ExpiresIn:    tokens.ExpiresIn,
	}, nil
}

type productProfileBody struct {
	AccountType       string  `json:"account_type" validate:"required,oneof=individual business"`
	FirstName         *string `json:"first_name"`
	LastName          *string `json:"last_name"`
	CountryID         *string `json:"country_id"`
	LegalBusinessName *string `json:"legal_business_name"`
	LegalFullName     *string `json:"legal_full_name"`
	CompanyRoleID     *string `json:"company_role_id"`
}

type productAddressBody struct {
	CountryID        string            `json:"country_id" validate:"required,uuid"`
	EntryMode        string            `json:"entry_mode" validate:"required,oneof=search manual"`
	Line1            string            `json:"line_1" validate:"required_if=EntryMode manual,max=200"`
	Line2            *string           `json:"line_2"`
	City             string            `json:"city" validate:"required,max=100"`
	StateOrCounty    string            `json:"state_or_county" validate:"required,max=100"`
	PostCode         string            `json:"post_code" validate:"required_if=EntryMode manual,max=20"`
	FormattedAddress *string           `json:"formatted_address"`
	Identification   map[string]string `json:"identification"`
}

type productVerifyAddressBody struct {
	CountryID        string  `json:"country_id" validate:"required,uuid"`
	EntryMode        string  `json:"entry_mode" validate:"required,oneof=search manual"`
	Line1            string  `json:"line_1" validate:"required,max=200"`
	Line2            *string `json:"line_2"`
	City             string  `json:"city" validate:"required,max=100"`
	StateOrCounty    string  `json:"state_or_county" validate:"required,max=100"`
	PostCode         string  `json:"post_code" validate:"required,max=20"`
	FormattedAddress *string `json:"formatted_address"`
	PlaceID          *string `json:"place_id"`
}

type addressSearchBody struct {
	Query     string `json:"query" validate:"required,min=2,max=200"`
	CountryID string `json:"country_id" validate:"required,uuid"`
}

type productBusinessBody struct {
	BusinessTypeID string `json:"business_type_id" validate:"required,uuid"`
	IndustryID     string `json:"industry_id" validate:"required,uuid"`
	EmployeeCount  int    `json:"employee_count" validate:"required,min=1,max=10000"`
}

// status godoc
//
//	@Summary		Get onboarding status
//	@Description	Returns current step, completed steps, and saved draft fields.
//	@Tags			onboarding/session
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.ProductOnboardingStatusEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/status [get]
func (h *ProductOnboardingHandler) status(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	data, err := h.loadStatus(r.Context(), reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, data)
}

// putProfile godoc
//
//	@Summary		Upsert profile
//	@Description	Saves account type and profile fields for individual or business accounts.
//	@Tags			onboarding/profile
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductProfileRequest	true	"Profile payload"
//	@Success		200		{object}	handler.ProductProfileEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/profile [put]
func (h *ProductOnboardingHandler) putProfile(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	var req productProfileBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()

	var (
		data ProductProfileData
		err  error
	)
	switch req.AccountType {
	case onboarding.AccountIndividual:
		data, err = h.saveIndividualProfile(ctx, userID, reg, &req)
	case onboarding.AccountBusiness:
		data, err = h.saveBusinessProfile(ctx, userID, reg, &req)
	default:
		err = apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, data)
}

// saveIndividualProfile stores an individual's profile and, for a still-global
// account, migrates it into the region of the chosen country.
func (h *ProductOnboardingHandler) saveIndividualProfile(
	ctx context.Context,
	userID uuid.UUID,
	reg region.Region,
	reqp *productProfileBody,
) (ProductProfileData, error) {
	req := *reqp
	originalReg := reg
	if err := h.validateIndividualProfile(req); err != nil {
		return ProductProfileData{}, err
	}
	countryID := uuid.MustParse(*req.CountryID)
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		return ProductProfileData{}, err
	}

	// Individual accounts always give their country at this step, so
	// this is the point a still-global account migrates into a real
	// region.
	if reg == region.RegionGlobal {
		newReg, err := h.moveToCountryRegion(ctx, userID, countryID)
		if err != nil {
			return ProductProfileData{}, err
		}
		reg = newReg
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	sql, args, err := query.UpdateAccountUserIndividualProfile(userID, *req.FirstName, *req.LastName, countryID)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

		completed, currentStep, err := h.advanceProgress(ctx, tx, reg, userID, onboarding.StepProfile)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

	completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepProfile)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	tokens, err := h.reissueIfMigrated(userID, originalReg, reg)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	next := onboarding.NextStep(req.AccountType, completed)
	return ProductProfileData{
		AccountType: req.AccountType,
		FirstName:   req.FirstName,
		LastName:    req.LastName,
		CountryID:   req.CountryID,
		Region:      string(reg),
		Onboarding: OnboardingProgressSummary{
			Status:         onboarding.StatusInProgress,
			CurrentStep:    &currentStep,
			CompletedSteps: completed,
			NextStep:       &next,
		},
		Tokens: tokens,
	}, nil
}

// saveBusinessProfile stores a business owner's profile and company details.
func (h *ProductOnboardingHandler) saveBusinessProfile(
	ctx context.Context,
	userID uuid.UUID,
	reg region.Region,
	reqp *productProfileBody,
) (ProductProfileData, error) {
	req := *reqp
	if err := h.validateBusinessProfile(req); err != nil {
		return ProductProfileData{}, err
	}
	roleID := uuid.MustParse(*req.CompanyRoleID)
	if err := h.ensureActiveCompanyRole(ctx, roleID); err != nil {
		return ProductProfileData{}, err
	}

	// Business accounts don't give a country until the address step, so
	// this write may still land in the global holding area.
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	userSQL, userArgs, err := updateBusinessProfileSQL(reg, userID, *req.LegalFullName)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, userSQL, userArgs...); execErr != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

		completed, currentStep, err := h.advanceProgress(ctx, tx, reg, userID, onboarding.StepProfile)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

	completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepProfile)
	if err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	if err := tx.Commit(ctx); err != nil {
		return ProductProfileData{}, apperror.ErrInternal
	}

	next := onboarding.NextStep(req.AccountType, completed)
	return ProductProfileData{
		AccountType:       req.AccountType,
		LegalBusinessName: req.LegalBusinessName,
		LegalFullName:     req.LegalFullName,
		CompanyRoleID:     req.CompanyRoleID,
		Region:            string(reg),
		Onboarding: OnboardingProgressSummary{
			Status:         onboarding.StatusInProgress,
			CurrentStep:    &currentStep,
			CompletedSteps: completed,
			NextStep:       &next,
		},
	}, nil
}

// putAddress godoc
//
//	@Summary		Upsert identification and address
//	@Description	Saves business registry identification and workspace address. Business accounts only.
//	@Tags			onboarding/address
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductAddressRequest	true	"Address payload"
//	@Success		200		{object}	handler.ProductAddressEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address [put]
func (h *ProductOnboardingHandler) putAddress(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	var req productAddressBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}
	if err := requireFormattedAddress(req.EntryMode, req.FormattedAddress); err != nil {
		response.Error(w, r, err)
		return
	}

	ctx := r.Context()
	data, err := h.saveAddress(ctx, userID, reg, &req)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, data)
}

// moveToCountryRegion migrates a still-global account into the region that serves
// the given country and returns the new region.
func (h *ProductOnboardingHandler) moveToCountryRegion(
	ctx context.Context,
	userID, countryID uuid.UUID,
) (region.Region, error) {
	target, err := h.countryRegion(ctx, countryID)
	if err != nil || !region.Valid(target) || target == region.RegionGlobal {
		return region.RegionUnknown, apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidCountryID)
	}
	newReg, err := migrateUserToRegion(ctx, h.Router, h.GlobalDB, userID, target)
	if err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	return newReg, nil
}

// saveAddress validates and stores a business's identification and address.
func (h *ProductOnboardingHandler) saveAddress(
	ctx context.Context,
	userID uuid.UUID,
	reg region.Region,
	reqp *productAddressBody,
) (ProductAddressData, error) {
	req := *reqp
	originalReg := reg
	countryID := uuid.MustParse(req.CountryID)
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		return ProductAddressData{}, err
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return ProductAddressData{}, apperror.ErrInternal
	}

	accountType, err := h.loadAccountType(ctx, pool, reg, userID)
	if err != nil {
		return ProductAddressData{}, apperror.ErrInternal
	}
	if accountType != onboarding.AccountBusiness {
		return ProductAddressData{}, apperror.New(apperror.CodeInvalidAccountBranch, apperror.MsgInvalidAccountTypeBranch)
	}

	// This is the first point a business account gives a country, so it's
	// where a still-global account migrates into a real region.
	if reg == region.RegionGlobal {
		reg, err = h.moveToCountryRegion(ctx, userID, countryID)
		if err != nil {
			return ProductAddressData{}, err
		}
		pool, err = resolvePool(h.Router, h.GlobalDB, reg)
		if err != nil {
			return ProductAddressData{}, apperror.ErrInternal
		}
	}

	identificationPayload, binNumber, err := h.checkIdentification(ctx, countryID, req.Identification)
	if err != nil {
		return ProductAddressData{}, err
	}

	completed, currentStep, err := h.persistAddress(ctx, pool, userID, countryID, &req, identificationPayload, binNumber)
	if err != nil {
		return ProductAddressData{}, err
	}

	tokens, err := h.reissueIfMigrated(userID, originalReg, reg)
	if err != nil {
		return ProductAddressData{}, apperror.ErrInternal
	}

	next := onboarding.NextStep(accountType, completed)
	return ProductAddressData{
		CountryID:          req.CountryID,
		EntryMode:          req.EntryMode,
		Line1:              req.Line1,
		Line2:              req.Line2,
		City:               req.City,
		StateOrCounty:      req.StateOrCounty,
		PostCode:           req.PostCode,
		FormattedAddress:   req.FormattedAddress,
		Identification:     req.Identification,
		VerificationStatus: "unverified",
		Onboarding: OnboardingProgressSummary{
			Status:         onboarding.StatusInProgress,
			CurrentStep:    &currentStep,
			CompletedSteps: completed,
			NextStep:       &next,
		},
		Tokens: tokens,
	}, nil
}

// checkIdentification validates the registry identification for the country and
// returns its JSON payload and the primary registry number for storage.
func (h *ProductOnboardingHandler) checkIdentification(
	ctx context.Context,
	countryID uuid.UUID,
	identification map[string]string,
) (payload []byte, binNumber string, err error) {
	countrySlug, err := h.countrySlugByID(ctx, countryID)
	if err != nil {
		return nil, "", err
	}
	if validateErr := onboarding.ValidateIdentification(countrySlug, identification); validateErr != nil {
		return nil, "", apperror.New(apperror.CodeValidationError, validateErr.Error())
	}
	payload, err = json.Marshal(identification)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}
	return payload, onboarding.PrimaryIdentificationNumber(countrySlug, identification), nil
}

// persistAddress writes the organization identification and address and advances
// onboarding progress in one transaction.
func (h *ProductOnboardingHandler) persistAddress(
	ctx context.Context,
	pool dataPool,
	userID, countryID uuid.UUID,
	req *productAddressBody,
	identificationPayload []byte,
	binNumber string,
) (completed []string, currentStep string, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	id, err := h.loadOrganizationID(ctx, tx, userID)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}
	orgID := &id

	idSQL, idArgs, err := query.UpdateOrganizationIdentification(userID, binNumber, identificationPayload)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, idSQL, idArgs...); execErr != nil {
		return nil, "", apperror.ErrInternal
	}

	addrSQL, addrArgs, err := query.UpsertAccountAddress(
		userID, orgID, countryID, req.EntryMode, req.Line1, req.Line2,
		req.City, req.StateOrCounty, req.PostCode, req.FormattedAddress,
	)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, addrSQL, addrArgs...); execErr != nil {
		return nil, "", apperror.ErrInternal
	}

	completed, currentStep, err := h.advanceProgress(ctx, tx, reg, userID, onboarding.StepIdentificationAddress)
	if err != nil {
		return nil, "", apperror.ErrInternal
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return nil, "", apperror.ErrInternal
	}
	return completed, currentStep, nil
}

// searchAddress godoc
//
//	@Summary		Search addresses
//	@Description	Returns address autocomplete suggestions for a partial query and country.
//	@Tags			onboarding/address
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.AddressSearchRequest	true	"Address search payload"
//	@Success		200		{object}	handler.AddressSearchEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		501		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address/search [post]
func (h *ProductOnboardingHandler) searchAddress(w http.ResponseWriter, r *http.Request) {
	if h.AddressSearcher == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotImplemented, apperror.MsgNotImplemented))
		return
	}

	_, _, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	var req addressSearchBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	countryID := uuid.MustParse(req.CountryID)
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		response.Error(w, r, err)
		return
	}

	results, err := h.AddressSearcher.Search(ctx, req.CountryID, strings.TrimSpace(req.Query))
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	suggestions := make([]AddressSuggestion, 0, len(results))
	for _, item := range results {
		suggestions = append(suggestions, AddressSuggestion{
			PlaceID:          item.PlaceID,
			Description:      item.Description,
			Line1:            item.Line1,
			Line2:            item.Line2,
			City:             item.City,
			StateOrCounty:    item.StateOrCounty,
			PostCode:         item.PostCode,
			FormattedAddress: item.FormattedAddress,
		})
	}

	response.Success(w, r, http.StatusOK, AddressSearchData{Suggestions: suggestions})
}

// verifyAddress godoc
//
//	@Summary		Verify address
//	@Description	Verifies a search or manual address payload. Returns verification_status and optional failure_reason (not_verifiable shows manual entry).
//	@Tags			onboarding/address
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.VerifyAddressRequest	true	"Address verification payload"
//	@Success		200	{object}	handler.VerifyAddressEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address/verify [post]
func (h *ProductOnboardingHandler) verifyAddress(w http.ResponseWriter, r *http.Request) {
	if h.AddressProvider == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotImplemented, apperror.MsgNotImplemented))
		return
	}

	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	var req productVerifyAddressBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}
	if err := requireFormattedAddress(req.EntryMode, req.FormattedAddress); err != nil {
		response.Error(w, r, err)
		return
	}

	ctx := r.Context()
	countryID := uuid.MustParse(req.CountryID)
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		response.Error(w, r, err)
		return
	}

	result, err := h.AddressProvider.Verify(ctx, onboarding.ProductAddress{
		CountryID:        req.CountryID,
		EntryMode:        req.EntryMode,
		Line1:            req.Line1,
		Line2:            req.Line2,
		City:             req.City,
		StateOrCounty:    req.StateOrCounty,
		PostCode:         req.PostCode,
		FormattedAddress: req.FormattedAddress,
		PlaceID:          req.PlaceID,
	})
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if result.Status == onboarding.VerificationStatusVerified {
		if err := storeVerifiedAddress(ctx, pool, userID, result.Status); err != nil {
			response.Error(w, r, err)
			return
		}
	}

	response.Success(w, r, http.StatusOK, VerifyAddressResponse{
		CountryID:          req.CountryID,
		EntryMode:          req.EntryMode,
		Line1:              req.Line1,
		Line2:              req.Line2,
		City:               req.City,
		StateOrCounty:      req.StateOrCounty,
		PostCode:           req.PostCode,
		FormattedAddress:   req.FormattedAddress,
		VerificationStatus: result.Status,
		FailureReason:      result.FailureReason,
	})
}

// requireFormattedAddress checks that a search-mode address carries the formatted address.
func requireFormattedAddress(entryMode string, formatted *string) error {
	if entryMode == "search" && (formatted == nil || strings.TrimSpace(*formatted) == "") {
		return apperror.New(apperror.CodeValidationError, "formatted_address is required when entry_mode is search")
	}
	return nil
}

// storeVerifiedAddress records the verification status on the user's address.
func storeVerifiedAddress(ctx context.Context, pool dataPool, userID uuid.UUID, status string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	sql, args, err := query.UpdateAccountAddressVerificationStatus(userID, status)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil && !errors.Is(execErr, pgx.ErrNoRows) {
		return apperror.ErrInternal
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apperror.ErrInternal
	}
	return nil
}

// writeComplianceDetails stores business type, industry and employee count on the
// individual's user row or on the business's organization row.
func writeComplianceDetails(
	ctx context.Context,
	tx pgx.Tx,
	accountType string,
	userID, businessTypeID, industryID uuid.UUID,
	req *productBusinessBody,
) error {
	switch accountType {
	case onboarding.AccountIndividual:
		sql, args, err := query.UpdateIndividualUserBusinessDetails(userID, businessTypeID, industryID, req.EmployeeCount)
		if err != nil {
			return apperror.ErrInternal
		}
		if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
			return apperror.ErrInternal
		}
	case onboarding.AccountBusiness:
		sql, args, err := query.UpdateOrganizationBusiness(userID, businessTypeID, industryID, req.EmployeeCount)
		if err != nil {
			return apperror.ErrInternal
		}
		if scanErr := tx.QueryRow(ctx, sql, args...).Scan(new(uuid.UUID)); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return apperror.New(apperror.CodeValidationError, "complete profile before compliance step")
			}
			return apperror.ErrInternal
		}
	default:
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	}
	return nil
}

// putCompliance godoc
//
//	@Summary		Upsert compliance
//	@Description	Saves business type, industry, and employee count for individual or business accounts.
//	@Tags			onboarding/compliance
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductComplianceRequest	true	"Compliance payload"
//	@Success		200		{object}	handler.ProductComplianceEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/compliance [put]
func (h *ProductOnboardingHandler) putCompliance(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	var req productBusinessBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	if reg == region.RegionGlobal {
		// Compliance always writes into the organization/user row for a real
		// region. Individual accounts get a region at the profile step and
		// business accounts at the address step, so reaching here while
		// still global means an earlier step was skipped.
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "complete the profile step before compliance"))
		return
	}

	ctx := r.Context()
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	accountType, err := h.loadAccountType(ctx, pool, reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	businessTypeID := uuid.MustParse(req.BusinessTypeID)
	industryID := uuid.MustParse(req.IndustryID)
	if ensureActiveBusinessTypeErr := h.ensureActiveBusinessType(
		ctx,
		businessTypeID,
	); ensureActiveBusinessTypeErr != nil {
		response.Error(w, r, ensureActiveBusinessTypeErr)
		return
	}
	if ensureActiveOnboardingIndustryErr := h.ensureActiveOnboardingIndustry(
		ctx,
		industryID,
	); ensureActiveOnboardingIndustryErr != nil {
		response.Error(w, r, ensureActiveOnboardingIndustryErr)
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, tx)

	if err = writeComplianceDetails(ctx, tx, accountType, userID, businessTypeID, industryID, &req); err != nil {
		response.Error(w, r, err)
		return
	}

	completed, currentStep, err := h.advanceProgress(ctx, tx, reg, userID, onboarding.StepCompliance)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	next := onboarding.NextStep(accountType, completed)
	response.Success(w, r, http.StatusOK, ProductComplianceData{
		BusinessTypeID: req.BusinessTypeID,
		IndustryID:     req.IndustryID,
		EmployeeCount:  req.EmployeeCount,
		Onboarding: OnboardingProgressSummary{
			Status:         onboarding.StatusInProgress,
			CurrentStep:    &currentStep,
			CompletedSteps: completed,
			NextStep:       &next,
		},
	})
}

// putBusiness godoc
//
//	@Summary		Upsert business compliance (deprecated)
//	@Description	Deprecated: use PUT /onboarding/compliance. Saves business type, industry, and employee count.
//	@Deprecated
//	@Tags			onboarding/business
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.ProductBusinessRequest	true	"Business payload"
//	@Success		200		{object}	handler.ProductBusinessEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/business [put]
func (h *ProductOnboardingHandler) putBusiness(w http.ResponseWriter, r *http.Request) {
	h.putCompliance(w, r)
}

// complete godoc
//
//	@Summary		Complete onboarding
//	@Description	Finalizes onboarding after all mandatory steps are done. Sets onboarding_completed_at and enqueues an onboarding_complete welcome email.
//	@Tags			onboarding/session
//	@Produce		json
//	@Security		BearerAuth
//	@Success		201	{object}	handler.ProductOnboardingCompleteEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/complete [post]
func (h *ProductOnboardingHandler) complete(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	if reg == region.RegionGlobal {
		response.Error(w, r, apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete))
		return
	}

	ctx := r.Context()
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	userSQL, userArgs, err := lookupUserByIDSQL(reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var completedAt *time.Time
	var accountType *string
	var email string
	if scanErr := pool.QueryRow(ctx, userSQL, userArgs...).Scan(
		&userID, &email, new(string), new(*time.Time), &completedAt,
		&accountType, new(*string), new(*string), new(*string), new(*uuid.UUID),
		new(*string), new(*time.Time),
	); scanErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if completedAt != nil {
		response.Error(w, r, apperror.ErrConflict)
		return
	}

	progressSQL, progressArgs, err := lookupOnboardingProgressSQL(reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var currentStep string
	var completed []string
	if scanErr := pool.QueryRow(ctx, progressSQL, progressArgs...).
		Scan(&userID, &currentStep, &completed); scanErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	completed = onboarding.NormalizeCompletedSteps(completed)

	if !onboarding.CanComplete(stringValue(accountType), completed) {
		response.Error(w, r, apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete))
		return
	}

	completeSQL, completeArgs, err := query.SetAccountOnboardingCompleted(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := pool.QueryRow(ctx, completeSQL, completeArgs...).Scan(&userID); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	slug := workspaceSlug(email)
	workspaceID := uuid.New()

	h.enqueueOnboardingComplete(ctx, email)

	response.Success(w, r, http.StatusCreated, ProductOnboardingCompleteData{
		Status: onboarding.StatusCompleted,
		Workspace: ProductWorkspaceStub{
			ID:   workspaceID.String(),
			Slug: slug,
		},
		RedirectURL: "/dashboard",
	})
}

func (h *ProductOnboardingHandler) enqueueOnboardingComplete(ctx context.Context, email string) {
	if h.Enqueuer == nil {
		return
	}
	tx, err := h.GlobalDB.Begin(ctx)
	if err != nil {
		return
	}
	defer rollbackOnError(ctx, tx)
	_, enqueueErr := h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:      mailer.TypeOnboardingComplete,
		Recipient: email,
	}, queue.EmailEnqueueOptions()...)
	besteffort.Log(ctx, "Enqueuer.EnqueueTx", enqueueErr)
	besteffort.Log(ctx, "tx.Commit", tx.Commit(ctx))
}

func (h *ProductOnboardingHandler) loadStatus(
	ctx context.Context,
	reg region.Region,
	userID uuid.UUID,
) (ProductOnboardingStatusData, error) {
	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}

	user, err := loadStatusUser(ctx, pool, reg, userID)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}
	status := onboarding.StatusInProgress
	if user.completedAt != nil {
		status = onboarding.StatusCompleted
	}

	currentStep, completed, err := loadStatusProgress(ctx, pool, reg, userID)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}

	next := onboarding.NextStep(stringValue(user.accountType), completed)
	data := ProductOnboardingStatusData{
		Status:         status,
		AccountType:    user.accountType,
		CurrentStep:    &currentStep,
		CompletedSteps: completed,
		NextStep:       &next,
	}

	if user.accountType != nil {
		data.Profile = loadProfileState(ctx, pool, reg, userID, &user)
	}
	data.Address = loadAddressState(ctx, pool, userID)

	if compliance := loadComplianceState(ctx, pool, userID, user.accountType); compliance != nil {
		data.Compliance = compliance
		data.Business = compliance
	}
	return data, nil
}

// statusUser is the part of the user row the onboarding status needs.
type statusUser struct {
	email         string
	completedAt   *time.Time
	accountType   *string
	firstName     *string
	lastName      *string
	legalFullName *string
	countryID     *uuid.UUID
}

func loadStatusUser(ctx context.Context, pool dataPool, reg region.Region, userID uuid.UUID) (statusUser, error) {
	sql, args, err := lookupUserByIDSQL(reg, userID)
	if err != nil {
		return statusUser{}, err
	}
	var u statusUser
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(
		new(uuid.UUID), &u.email, new(string), new(*time.Time), &u.completedAt,
		&u.accountType, &u.firstName, &u.lastName, &u.legalFullName, &u.countryID,
		new(*string), new(*time.Time),
	); scanErr != nil {
		return statusUser{}, scanErr
	}
	return u, nil
}

// loadStatusProgress returns the normalized onboarding progress, or the initial
// progress when none is stored yet.
func loadStatusProgress(
	ctx context.Context,
	pool dataPool,
	reg region.Region,
	userID uuid.UUID,
) (currentStep string, completed []string, err error) {
	sql, args, err := lookupOnboardingProgressSQL(reg, userID)
	if err != nil {
		return "", nil, err
	}
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(new(uuid.UUID), &currentStep, &completed); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return "", nil, scanErr
		}
		currentStep, completed = onboarding.InitialProgress()
	}
	return normalizeCurrentStep(currentStep), onboarding.NormalizeCompletedSteps(completed), nil
}

// loadProfileState builds the saved profile for the status response. Business
// accounts also get their organization name and company role when present.
func loadProfileState(
	ctx context.Context,
	pool dataPool,
	reg region.Region,
	userID uuid.UUID,
	user *statusUser,
) *ProductProfileState {
	profile := &ProductProfileState{
		AccountType:   *user.accountType,
		FirstName:     user.firstName,
		LastName:      user.lastName,
		LegalFullName: user.legalFullName,
	}
	if user.countryID != nil {
		s := user.countryID.String()
		profile.CountryID = &s
	}
	if *user.accountType != onboarding.AccountBusiness {
		return profile
	}

	var (
		sql  string
		args []any
		err  error
	)
	if reg == region.RegionGlobal {
		sql, args, err = query.LookupPendingOrganizationByOwnerWide(userID)
	} else {
		sql, args, err = query.LookupOrganizationByOwner(userID)
	}
	if err != nil {
		return profile
	}
	var legalName string
	var roleID uuid.UUID
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(
		new(uuid.UUID), &legalName, &roleID, new(*uuid.UUID), new(*uuid.UUID), new(*int),
	); scanErr == nil {
		profile.LegalBusinessName = &legalName
		role := roleID.String()
		profile.CompanyRoleID = &role
	}
	return profile
}

// loadAddressState returns the saved address, with registry identification when
// present, or nil when the user has no saved address. Lookup failures are treated as absent.
func loadAddressState(ctx context.Context, pool dataPool, userID uuid.UUID) *ProductAddressState {
	sql, args, err := query.LookupAccountAddressByUser(userID)
	if err != nil {
		return nil
	}
	var country uuid.UUID
	var entryMode, line1, city, state, postCode, verification string
	var line2, formatted *string
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(
		new(uuid.UUID), &country, &entryMode, &line1, &line2, &city, &state, &postCode, &formatted, &verification,
	); scanErr != nil {
		return nil
	}
	return &ProductAddressState{
		CountryID:          country.String(),
		EntryMode:          entryMode,
		Line1:              line1,
		Line2:              line2,
		City:               city,
		StateOrCounty:      state,
		PostCode:           postCode,
		FormattedAddress:   formatted,
		VerificationStatus: verification,
		Identification:     loadIdentification(ctx, pool, userID),
	}
}

func loadIdentification(ctx context.Context, pool dataPool, userID uuid.UUID) map[string]string {
	sql, args, err := query.LookupOrganizationIdentification(userID)
	if err != nil {
		return nil
	}
	var payload []byte
	if scanErr := pool.QueryRow(ctx, sql, args...).Scan(new(*string), &payload); scanErr != nil || len(payload) == 0 {
		return nil
	}
	var identification map[string]string
	if json.Unmarshal(payload, &identification) != nil {
		return nil
	}
	return identification
}

// loadComplianceState returns the saved compliance details, or nil when the
// account type has none or they are incomplete.
func loadComplianceState(
	ctx context.Context,
	pool dataPool,
	userID uuid.UUID,
	accountType *string,
) *ProductComplianceState {
	if accountType == nil {
		return nil
	}
	var businessTypeID, industryID *uuid.UUID
	var employeeCount *int
	switch *accountType {
	case onboarding.AccountIndividual:
		sql, args, err := query.LookupIndividualUserCompliance(userID)
		if err != nil {
			return nil
		}
		if pool.QueryRow(ctx, sql, args...).Scan(&businessTypeID, &industryID, &employeeCount) != nil {
			return nil
		}
	case onboarding.AccountBusiness:
		sql, args, err := query.LookupOrganizationByOwner(userID)
		if err != nil {
			return nil
		}
		if pool.QueryRow(ctx, sql, args...).Scan(
			new(uuid.UUID), new(string), new(uuid.UUID), &businessTypeID, &industryID, &employeeCount,
		) != nil {
			return nil
		}
	default:
		return nil
	}
	if businessTypeID == nil || industryID == nil || employeeCount == nil {
		return nil
	}
	return &ProductComplianceState{
		BusinessTypeID: businessTypeID.String(),
		IndustryID:     industryID.String(),
		EmployeeCount:  *employeeCount,
	}
}

func (h *ProductOnboardingHandler) advanceProgress(ctx context.Context, tx pgx.Tx, reg region.Region, userID uuid.UUID, step string) ([]string, string, error) {
	progressSQL, progressArgs, err := lookupOnboardingProgressSQL(reg, userID)
	if err != nil {
		return nil, "", err
	}

	var currentStep string
	var completed []string
	if scanErr := tx.QueryRow(ctx, progressSQL, progressArgs...).
		Scan(&userID, &currentStep, &completed); scanErr != nil {
		if !errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, "", scanErr
		}
		currentStep, completed = onboarding.InitialProgress()
	}
	completed = onboarding.NormalizeCompletedSteps(completed)

	completed = onboarding.AdvanceCompleted(completed, step)
	currentStep = normalizeStep(step)
	upsertSQL, upsertArgs, err := upsertOnboardingProgressSQL(reg, userID, currentStep, completed)
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, upsertSQL, upsertArgs...); err != nil {
		return nil, "", err
	}

	return completed, currentStep, nil
}

func (h *ProductOnboardingHandler) validateIndividualProfile(req productProfileBody) error {
	if req.FirstName == nil || strings.TrimSpace(*req.FirstName) == "" {
		return apperror.New(apperror.CodeValidationError, "first_name is required")
	}
	if req.LastName == nil || strings.TrimSpace(*req.LastName) == "" {
		return apperror.New(apperror.CodeValidationError, "last_name is required")
	}
	if req.CountryID == nil || !isUUID(*req.CountryID) {
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidCountryID)
	}
	return nil
}

func (h *ProductOnboardingHandler) validateBusinessProfile(req productProfileBody) error {
	if req.LegalBusinessName == nil || strings.TrimSpace(*req.LegalBusinessName) == "" {
		return apperror.New(apperror.CodeValidationError, "legal_business_name is required")
	}
	if req.LegalFullName == nil || strings.TrimSpace(*req.LegalFullName) == "" {
		return apperror.New(apperror.CodeValidationError, "legal_full_name is required")
	}
	if req.CompanyRoleID == nil || !isUUID(*req.CompanyRoleID) {
		return apperror.New(apperror.CodeValidationError, "company_role_id is required")
	}
	return nil
}

func (h *ProductOnboardingHandler) countrySlugByID(ctx context.Context, countryID uuid.UUID) (string, error) {
	sql, args, err := query.LookupCountryByID(countryID)
	if err != nil {
		return "", apperror.ErrInternal
	}
	var slug string
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).
		Scan(new(uuid.UUID), new(string), &slug, new(string), new(*string)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidCountryID)
		}
		return "", apperror.ErrInternal
	}
	return slug, nil
}

func normalizeCurrentStep(step string) string {
	switch step {
	case onboarding.StepAddress:
		return onboarding.StepIdentificationAddress
	case onboarding.StepBusiness:
		return onboarding.StepCompliance
	default:
		return step
	}
}

func normalizeStep(step string) string {
	return normalizeCurrentStep(step)
}

func (h *ProductOnboardingHandler) ensureActiveCountry(ctx context.Context, countryID uuid.UUID) error {
	sql, args, err := query.LookupCountryByID(countryID)
	if err != nil {
		return apperror.ErrInternal
	}
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).
		Scan(new(uuid.UUID), new(string), new(string), new(string), new(*string)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidCountryID)
		}
		return apperror.ErrInternal
	}
	return nil
}

func (h *ProductOnboardingHandler) countryRegion(ctx context.Context, countryID uuid.UUID) (region.Region, error) {
	sql, args, err := query.LookupCountryByID(countryID)
	if err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	var reg string
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).
		Scan(new(uuid.UUID), new(string), new(string), &reg, new(*string)); err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	return region.Region(reg), nil
}

func (h *ProductOnboardingHandler) ensureActiveCompanyRole(ctx context.Context, roleID uuid.UUID) error {
	return h.ensureCatalogRowByID(ctx, roleID, query.CompanyRoleByID)
}

func (h *ProductOnboardingHandler) ensureActiveBusinessType(ctx context.Context, id uuid.UUID) error {
	return h.ensureCatalogRowByID(ctx, id, query.BusinessTypeByID)
}

func (h *ProductOnboardingHandler) ensureActiveOnboardingIndustry(ctx context.Context, id uuid.UUID) error {
	return h.ensureCatalogRowByID(ctx, id, query.OnboardingIndustryByID)
}

func (h *ProductOnboardingHandler) ensureCatalogRowByID(
	ctx context.Context,
	id uuid.UUID,
	builder func(uuid.UUID) (string, []any, error),
) error {
	sql, args, err := builder(id)
	if err != nil {
		return apperror.ErrInternal
	}
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(new(uuid.UUID), new(string)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidRequest)
		}
		return apperror.ErrInternal
	}
	return nil
}

func (h *ProductOnboardingHandler) loadAccountType(
	ctx context.Context,
	pool dataPool,
	reg region.Region,
	userID uuid.UUID,
) (string, error) {
	sql, args, err := lookupUserByIDSQL(reg, userID)
	if err != nil {
		return "", err
	}
	var accountType *string
	if err := pool.QueryRow(ctx, sql, args...).Scan(
		&userID, new(string), new(string), new(*time.Time), new(*time.Time),
		&accountType, new(*string), new(*string), new(*string), new(*uuid.UUID),
		new(*string), new(*time.Time),
	); err != nil {
		return "", err
	}
	if accountType == nil {
		return "", fmt.Errorf("account type not set")
	}
	return *accountType, nil
}

func (h *ProductOnboardingHandler) loadOrganizationID(
	ctx context.Context,
	tx pgx.Tx,
	userID uuid.UUID,
) (uuid.UUID, error) {
	sql, args, err := query.LookupOrganizationByOwner(userID)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, sql, args...).
		Scan(&id, new(string), new(uuid.UUID), new(*uuid.UUID), new(*uuid.UUID), new(*int)); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

var slugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

func workspaceSlug(email string) string {
	base := strings.Split(email, "@")[0]
	base = strings.ToLower(base)
	base = slugSanitizer.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "workspace"
	}
	return base
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
