package handler

import (
	"context"
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
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// AddressVerifier checks a saved address against a third-party verification
// provider. No implementation is wired up yet; once one exists, set it on
// ProductOnboardingHandler.AddressProvider to enable POST /onboarding/address/verify.
type AddressVerifier interface {
	Verify(ctx context.Context, addr ProductAddressState) (status string, err error)
}

// ProductOnboardingHandler handles product onboarding endpoints.
type ProductOnboardingHandler struct {
	Router          *dbrouter.DBRouter
	GlobalDB        globalDB
	Enqueuer        jobEnqueuer
	AddressProvider AddressVerifier
	validate        *validator.Validate
}

// NewProductOnboardingHandler returns a product onboarding handler.
func NewProductOnboardingHandler(router *dbrouter.DBRouter, globalDB globalDB, enqueuer jobEnqueuer) *ProductOnboardingHandler {
	return &ProductOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Enqueuer: enqueuer,
		validate: validator.New(),
	}
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
	CountryID        string  `json:"country_id" validate:"required,uuid"`
	EntryMode        string  `json:"entry_mode" validate:"required,oneof=search manual"`
	Line1            string  `json:"line_1" validate:"required,max=200"`
	Line2            *string `json:"line_2"`
	City             string  `json:"city" validate:"required,max=100"`
	StateOrCounty    string  `json:"state_or_county" validate:"required,max=100"`
	PostCode         string  `json:"post_code" validate:"required,max=20"`
	FormattedAddress *string `json:"formatted_address"`
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
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	switch req.AccountType {
	case onboarding.AccountIndividual:
		if err := h.validateIndividualProfile(req); err != nil {
			response.Error(w, r, err)
			return
		}
		countryID := uuid.MustParse(*req.CountryID)
		if err := h.ensureActiveCountry(ctx, countryID); err != nil {
			response.Error(w, r, err)
			return
		}

		countryRegion, err := h.countryRegion(ctx, countryID)
		if err != nil {
			response.Error(w, r, err)
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		defer rollbackOnError(ctx, tx)

		sql, args, err := query.UpdateAccountUserIndividualProfile(userID, *req.FirstName, *req.LastName, countryID)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		if _, err := tx.Exec(ctx, `DELETE FROM organizations WHERE owner_user_id = $1`, userID); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepProfile)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		if err := tx.Commit(ctx); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		if err := h.updateRegistryRegion(ctx, emailFromUser(ctx, pool, userID), countryRegion); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		next := onboarding.NextStep(req.AccountType, completed)
		response.Success(w, r, http.StatusOK, ProductProfileData{
			AccountType: req.AccountType,
			FirstName:   req.FirstName,
			LastName:    req.LastName,
			CountryID:   req.CountryID,
			Region:      string(countryRegion),
			Onboarding: OnboardingProgressSummary{
				Status:         onboarding.StatusInProgress,
				CurrentStep:    &currentStep,
				CompletedSteps: completed,
				NextStep:       &next,
			},
		})

	case onboarding.AccountBusiness:
		if err := h.validateBusinessProfile(req); err != nil {
			response.Error(w, r, err)
			return
		}
		roleID := uuid.MustParse(*req.CompanyRoleID)
		if err := h.ensureActiveCompanyRole(ctx, roleID); err != nil {
			response.Error(w, r, err)
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		defer rollbackOnError(ctx, tx)

		userSQL, userArgs, err := query.UpdateAccountUserBusinessProfile(userID, *req.LegalFullName)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if _, err := tx.Exec(ctx, userSQL, userArgs...); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		orgSQL, orgArgs, err := query.UpsertOrganizationProfile(userID, *req.LegalBusinessName, roleID)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		if _, err := tx.Exec(ctx, orgSQL, orgArgs...); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepProfile)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		if err := tx.Commit(ctx); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}

		next := onboarding.NextStep(req.AccountType, completed)
		response.Success(w, r, http.StatusOK, ProductProfileData{
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
		})
	default:
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest))
	}
}

// putAddress godoc
//
//	@Summary		Upsert address
//	@Description	Saves workspace address from search or manual entry.
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
	if req.EntryMode == "search" && (req.FormattedAddress == nil || strings.TrimSpace(*req.FormattedAddress) == "") {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "formatted_address is required when entry_mode is search"))
		return
	}

	ctx := r.Context()
	countryID := uuid.MustParse(req.CountryID)
	if err := h.ensureActiveCountry(ctx, countryID); err != nil {
		response.Error(w, r, err)
		return
	}

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	accountType, err := h.loadAccountType(ctx, pool, userID)
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

	var orgID *uuid.UUID
	if accountType == onboarding.AccountBusiness {
		id, err := h.loadOrganizationID(ctx, tx, userID)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		orgID = &id
	}

	addrSQL, addrArgs, err := query.UpsertAccountAddress(
		userID, orgID, countryID, req.EntryMode, req.Line1, req.Line2,
		req.City, req.StateOrCounty, req.PostCode, req.FormattedAddress,
	)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, addrSQL, addrArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepAddress)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	next := onboarding.NextStep(accountType, completed)
	response.Success(w, r, http.StatusOK, ProductAddressData{
		CountryID:          req.CountryID,
		EntryMode:          req.EntryMode,
		Line1:              req.Line1,
		Line2:              req.Line2,
		City:               req.City,
		StateOrCounty:      req.StateOrCounty,
		PostCode:           req.PostCode,
		FormattedAddress:   req.FormattedAddress,
		VerificationStatus: "unverified",
		Onboarding: OnboardingProgressSummary{
			Status:         onboarding.StatusInProgress,
			CurrentStep:    &currentStep,
			CompletedSteps: completed,
			NextStep:       &next,
		},
	})
}

// verifyAddress godoc
//
//	@Summary		Verify address
//	@Description	Verifies the saved workspace address against a third-party provider. Returns 501 until a provider is integrated (see ProductOnboardingHandler.AddressProvider).
//	@Tags			onboarding/address
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	handler.VerifyAddressEnvelope
//	@Failure		400	{object}	apidoc.ErrorEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Failure		501	{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/address/verify [post]
func (h *ProductOnboardingHandler) verifyAddress(w http.ResponseWriter, r *http.Request) {
	// TODO: remove this gate once AddressProvider has a real third-party
	// implementation wired up (e.g. in NewProductOnboardingHandler). Everything
	// below is already wired to load the saved address, call the provider, and
	// persist its result.
	if h.AddressProvider == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotImplemented, apperror.MsgNotImplemented))
		return
	}

	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	ctx := r.Context()

	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	addrSQL, addrArgs, err := query.LookupAccountAddressByUser(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var addr ProductAddressState
	var addressID, countryID uuid.UUID
	if err := pool.QueryRow(ctx, addrSQL, addrArgs...).Scan(
		&addressID, &countryID, &addr.EntryMode, &addr.Line1, &addr.Line2,
		&addr.City, &addr.StateOrCounty, &addr.PostCode, &addr.FormattedAddress, &addr.VerificationStatus,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "complete address step before verification"))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	addr.CountryID = countryID.String()

	status, err := h.AddressProvider.Verify(ctx, addr)
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

	updSQL, updArgs, err := query.UpdateAccountAddressVerificationStatus(userID, status)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, updSQL, updArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	response.Success(w, r, http.StatusOK, VerifyAddressResponse{
		CountryID:          addr.CountryID,
		Line1:              addr.Line1,
		Line2:              addr.Line2,
		City:               addr.City,
		StateOrCounty:      addr.StateOrCounty,
		PostCode:           addr.PostCode,
		VerificationStatus: status,
	})
}

// putBusiness godoc
//
//	@Summary		Upsert business compliance
//	@Description	Saves business type, industry, and employee count. Business accounts only.
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

	ctx := r.Context()
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	accountType, err := h.loadAccountType(ctx, pool, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if accountType != onboarding.AccountBusiness {
		response.Error(w, r, apperror.New(apperror.CodeInvalidAccountBranch, apperror.MsgInvalidAccountTypeBranch))
		return
	}

	businessTypeID := uuid.MustParse(req.BusinessTypeID)
	industryID := uuid.MustParse(req.IndustryID)
	if err := h.ensureActiveBusinessType(ctx, businessTypeID); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureActiveOnboardingIndustry(ctx, industryID); err != nil {
		response.Error(w, r, err)
		return
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	defer rollbackOnError(ctx, tx)

	bizSQL, bizArgs, err := query.UpdateOrganizationBusiness(userID, businessTypeID, industryID, req.EmployeeCount)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := tx.QueryRow(ctx, bizSQL, bizArgs...).Scan(new(uuid.UUID)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			response.Error(w, r, apperror.New(apperror.CodeValidationError, "complete profile before business step"))
			return
		}
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	completed, currentStep, err := h.advanceProgress(ctx, tx, userID, onboarding.StepBusiness)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	next := onboarding.NextStep(accountType, completed)
	response.Success(w, r, http.StatusOK, ProductBusinessData{
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

	ctx := r.Context()
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	userSQL, userArgs, err := query.LookupAccountUserByID(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	var completedAt *time.Time
	var accountType *string
	var email string
	if err := pool.QueryRow(ctx, userSQL, userArgs...).Scan(
		&userID, &email, new(string), new(*time.Time), &completedAt,
		&accountType, new(*string), new(*string), new(*string), new(*uuid.UUID),
	); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if completedAt != nil {
		response.Error(w, r, apperror.ErrConflict)
		return
	}

	progressSQL, progressArgs, err := query.LookupOnboardingProgress(userID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	var currentStep string
	var completed []string
	if err := pool.QueryRow(ctx, progressSQL, progressArgs...).Scan(&userID, &currentStep, &completed); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

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
	_, _ = h.Enqueuer.EnqueueTx(ctx, tx, mailer.EmailArgs{
		Type:      mailer.TypeOnboardingComplete,
		Recipient: email,
	}, queue.EmailEnqueueOptions()...)
	_ = tx.Commit(ctx)
}

func (h *ProductOnboardingHandler) loadStatus(ctx context.Context, reg region.Region, userID uuid.UUID) (ProductOnboardingStatusData, error) {
	pool, err := h.Router.DB(reg)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}

	userSQL, userArgs, err := query.LookupAccountUserByID(userID)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}

	var email string
	var completedAt *time.Time
	var accountType *string
	var firstName, lastName, legalFullName *string
	var countryID *uuid.UUID
	if err := pool.QueryRow(ctx, userSQL, userArgs...).Scan(
		&userID, &email, new(string), new(*time.Time), &completedAt,
		&accountType, &firstName, &lastName, &legalFullName, &countryID,
	); err != nil {
		return ProductOnboardingStatusData{}, err
	}

	status := onboarding.StatusInProgress
	if completedAt != nil {
		status = onboarding.StatusCompleted
	}

	progressSQL, progressArgs, err := query.LookupOnboardingProgress(userID)
	if err != nil {
		return ProductOnboardingStatusData{}, err
	}

	var currentStep string
	var completed []string
	if err := pool.QueryRow(ctx, progressSQL, progressArgs...).Scan(&userID, &currentStep, &completed); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return ProductOnboardingStatusData{}, err
		}
		currentStep, completed = onboarding.InitialProgress()
	}

	next := onboarding.NextStep(stringValue(accountType), completed)
	data := ProductOnboardingStatusData{
		Status:         status,
		AccountType:    accountType,
		CurrentStep:    &currentStep,
		CompletedSteps: completed,
		NextStep:       &next,
	}

	if accountType != nil {
		profile := &ProductProfileState{AccountType: *accountType}
		if firstName != nil {
			profile.FirstName = firstName
		}
		if lastName != nil {
			profile.LastName = lastName
		}
		if countryID != nil {
			s := countryID.String()
			profile.CountryID = &s
		}
		if legalFullName != nil {
			profile.LegalFullName = legalFullName
		}
		if *accountType == onboarding.AccountBusiness {
			orgSQL, orgArgs, err := query.LookupOrganizationByOwner(userID)
			if err == nil {
				var legalName string
				var roleID uuid.UUID
				var businessTypeID, industryID *uuid.UUID
				var employeeCount *int
				if err := pool.QueryRow(ctx, orgSQL, orgArgs...).Scan(
					new(uuid.UUID), &legalName, &roleID, &businessTypeID, &industryID, &employeeCount,
				); err == nil {
					profile.LegalBusinessName = &legalName
					role := roleID.String()
					profile.CompanyRoleID = &role
				}
			}
		}
		data.Profile = profile
	}

	addrSQL, addrArgs, err := query.LookupAccountAddressByUser(userID)
	if err == nil {
		var country uuid.UUID
		var entryMode, line1, city, state, postCode, verification string
		var line2, formatted *string
		if err := pool.QueryRow(ctx, addrSQL, addrArgs...).Scan(
			new(uuid.UUID), &country, &entryMode, &line1, &line2, &city, &state, &postCode, &formatted, &verification,
		); err == nil {
			cid := country.String()
			data.Address = &ProductAddressState{
				CountryID:          cid,
				EntryMode:          entryMode,
				Line1:              line1,
				Line2:              line2,
				City:               city,
				StateOrCounty:      state,
				PostCode:           postCode,
				FormattedAddress:   formatted,
				VerificationStatus: verification,
			}
		}
	}

	if accountType != nil && *accountType == onboarding.AccountBusiness {
		orgSQL, orgArgs, err := query.LookupOrganizationByOwner(userID)
		if err == nil {
			var businessTypeID, industryID *uuid.UUID
			var employeeCount *int
			if err := pool.QueryRow(ctx, orgSQL, orgArgs...).Scan(
				new(uuid.UUID), new(string), new(uuid.UUID), &businessTypeID, &industryID, &employeeCount,
			); err == nil && businessTypeID != nil && industryID != nil && employeeCount != nil {
				data.Business = &ProductBusinessState{
					BusinessTypeID: businessTypeID.String(),
					IndustryID:     industryID.String(),
					EmployeeCount:  *employeeCount,
				}
			}
		}
	}

	return data, nil
}

func (h *ProductOnboardingHandler) advanceProgress(ctx context.Context, tx pgx.Tx, userID uuid.UUID, step string) ([]string, string, error) {
	progressSQL, progressArgs, err := query.LookupOnboardingProgress(userID)
	if err != nil {
		return nil, "", err
	}

	var currentStep string
	var completed []string
	if err := tx.QueryRow(ctx, progressSQL, progressArgs...).Scan(&userID, &currentStep, &completed); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, "", err
		}
		currentStep, completed = onboarding.InitialProgress()
	}

	completed = onboarding.AdvanceCompleted(completed, step)
	currentStep = step
	upsertSQL, upsertArgs, err := query.UpsertOnboardingProgress(userID, currentStep, completed)
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

func (h *ProductOnboardingHandler) ensureActiveCountry(ctx context.Context, countryID uuid.UUID) error {
	sql, args, err := query.LookupCountryByID(countryID)
	if err != nil {
		return apperror.ErrInternal
	}
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(new(uuid.UUID), new(string), new(string), new(string), new(*string)); err != nil {
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
	if err := h.GlobalDB.QueryRow(ctx, sql, args...).Scan(new(uuid.UUID), new(string), new(string), &reg, new(*string)); err != nil {
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

func (h *ProductOnboardingHandler) ensureCatalogRowByID(ctx context.Context, id uuid.UUID, builder func(uuid.UUID) (string, []any, error)) error {
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

func (h *ProductOnboardingHandler) loadAccountType(ctx context.Context, pool dbrouter.PgxPool, userID uuid.UUID) (string, error) {
	sql, args, err := query.LookupAccountUserByID(userID)
	if err != nil {
		return "", err
	}
	var accountType *string
	if err := pool.QueryRow(ctx, sql, args...).Scan(
		&userID, new(string), new(string), new(*time.Time), new(*time.Time),
		&accountType, new(*string), new(*string), new(*string), new(*uuid.UUID),
	); err != nil {
		return "", err
	}
	if accountType == nil {
		return "", fmt.Errorf("account type not set")
	}
	return *accountType, nil
}

func (h *ProductOnboardingHandler) loadOrganizationID(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (uuid.UUID, error) {
	sql, args, err := query.LookupOrganizationByOwner(userID)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, sql, args...).Scan(&id, new(string), new(uuid.UUID), new(*uuid.UUID), new(*uuid.UUID), new(*int)); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (h *ProductOnboardingHandler) updateRegistryRegion(ctx context.Context, email string, reg region.Region) error {
	sql, args, err := query.UpdateUsersRegistryRegion(email, string(reg), regionSourceExplicit)
	if err != nil {
		return err
	}
	_, err = h.GlobalDB.Exec(ctx, sql, args...)
	return err
}

func emailFromUser(ctx context.Context, pool dbrouter.PgxPool, userID uuid.UUID) string {
	sql, args, _ := query.LookupAccountUserByID(userID)
	var email string
	_ = pool.QueryRow(ctx, sql, args...).Scan(&userID, &email, new(string), new(*time.Time), new(*time.Time), new(*string), new(*string), new(*string), new(*string), new(*uuid.UUID))
	return email
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
