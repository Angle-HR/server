package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/query"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// BusinessOnboardingHandler handles the business onboarding endpoint.
type BusinessOnboardingHandler struct {
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	Tokens   *auth.TokenService
	validate *validator.Validate
}

// NewBusinessOnboardingHandler returns a business onboarding handler. Tokens
// is used to reissue JWTs when this submission migrates the account out of
// the global holding region (see NewProductOnboardingHandler).
func NewBusinessOnboardingHandler(router *dbrouter.DBRouter, globalDB globalDB, tokens *auth.TokenService) *BusinessOnboardingHandler {
	return &BusinessOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Tokens:   tokens,
		validate: validator.New(),
	}
}

// RegisterProtectedRoutes mounts the authenticated business onboarding route.
func (h *BusinessOnboardingHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Post("/onboarding/business", h.submitBusinessOnboarding)
}

type businessOnboardingRequest struct {
	LegalBusinessName         string `json:"legal_business_name"          validate:"required,max=200"`
	LegalFullName             string `json:"legal_full_name"              validate:"required,max=200"`
	CountryID                 string `json:"country_id"                   validate:"required,uuid"`
	CompanyRoleID             string `json:"company_role_id"              validate:"required,uuid"`
	BINumber                  string `json:"bin_number"                   validate:"required,max=50"`
	BusinessRegisteredAddress string `json:"business_registered_address"  validate:"required,max=300"`
	BusinessTypeID            string `json:"business_type_id"             validate:"required,uuid"`
	IndustryID                string `json:"industry_id"                  validate:"required,uuid"`
}

func (r *businessOnboardingRequest) trim() {
	r.LegalBusinessName = strings.TrimSpace(r.LegalBusinessName)
	r.LegalFullName = strings.TrimSpace(r.LegalFullName)
	r.CountryID = strings.TrimSpace(r.CountryID)
	r.CompanyRoleID = strings.TrimSpace(r.CompanyRoleID)
	r.BINumber = strings.TrimSpace(r.BINumber)
	r.BusinessRegisteredAddress = strings.TrimSpace(r.BusinessRegisteredAddress)
	r.BusinessTypeID = strings.TrimSpace(r.BusinessTypeID)
	r.IndustryID = strings.TrimSpace(r.IndustryID)
}

// submitBusinessOnboarding godoc
//
//	@Summary		Submit business onboarding (deprecated)
//	@Description	Deprecated: prefer stepped PUT /onboarding/profile → address → PUT /onboarding/business (type/industry/employees) → complete. This one-shot includes KYB-oriented fields (BIN, registered address) and does not advance onboarding progress.
//	@Deprecated
//	@Tags			onboarding/business
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			body	body		handler.BusinessRequest	true	"Business onboarding payload"
//	@Success		201		{object}	handler.BusinessEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/onboarding/business [post]
func (h *BusinessOnboardingHandler) submitBusinessOnboarding(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	var req businessOnboardingRequest
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
	companyRoleID := uuid.MustParse(req.CompanyRoleID)
	businessTypeID := uuid.MustParse(req.BusinessTypeID)
	industryID := uuid.MustParse(req.IndustryID)

	// Validate each referenced catalog entry exists.
	if err := h.ensureBusinessOnboardingCatalogRef(ctx, countryID, query.LookupCountryByID, apperror.MsgInvalidCountryID); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureBusinessOnboardingCatalogRef(ctx, companyRoleID, query.CompanyRoleByID, "unknown company_role_id reference"); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureBusinessOnboardingCatalogRef(ctx, businessTypeID, query.BusinessTypeByID, "unknown business_type_id reference"); err != nil {
		response.Error(w, r, err)
		return
	}
	if err := h.ensureBusinessOnboardingCatalogRef(ctx, industryID, query.OnboardingIndustryByID, "unknown industry_id reference"); err != nil {
		response.Error(w, r, err)
		return
	}

	// This one-shot submission includes the country directly, so a still-
	// global account migrates into its real region right here.
	originalReg := reg
	if reg == region.RegionGlobal {
		countrySQL, countryArgs, err := query.LookupCountryByID(countryID)
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		var target string
		if err := h.GlobalDB.QueryRow(ctx, countrySQL, countryArgs...).Scan(new(uuid.UUID), new(string), new(string), &target, new(*string)); err != nil {
			response.Error(w, r, apperror.New(apperror.CodeInvalidReference, apperror.MsgInvalidCountryID))
			return
		}
		newReg, err := migrateUserToRegion(ctx, h.Router, h.GlobalDB, userID, region.Region(target))
		if err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		reg = newReg
	}

	pool, err := resolvePool(h.Router, h.GlobalDB, reg)
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

	// Persist business profile fields on the user row.
	userSQL, userArgs, err := query.UpdateAccountUserBusinessProfile(userID, req.LegalFullName)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, userSQL, userArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	// Create or update the organization row owned by this user.
	orgSQL, orgArgs, err := query.UpsertOrganizationProfile(userID, req.LegalBusinessName, companyRoleID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if _, err := tx.Exec(ctx, orgSQL, orgArgs...); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	// Save business type and industry on the organization row.
	bizSQL, bizArgs, err := query.UpdateOrganizationCatalog(userID, businessTypeID, industryID)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := tx.QueryRow(ctx, bizSQL, bizArgs...).Scan(new(uuid.UUID)); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	// Save country, BIN number, and registered address on the organization row.
	regSQL, regArgs, err := query.UpdateOrganizationRegistration(userID, countryID, req.BINumber, req.BusinessRegisteredAddress)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if err := tx.QueryRow(ctx, regSQL, regArgs...).Scan(new(uuid.UUID)); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		response.Error(w, r, apperror.ErrInternal)
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

	response.Success(w, r, http.StatusCreated, BusinessResponse{
		UserID:                    userID.String(),
		LegalBusinessName:         req.LegalBusinessName,
		LegalFullName:             req.LegalFullName,
		CountryID:                 req.CountryID,
		CompanyRoleID:             req.CompanyRoleID,
		BINumber:                  req.BINumber,
		BusinessRegisteredAddress: req.BusinessRegisteredAddress,
		BusinessTypeID:            req.BusinessTypeID,
		IndustryID:                req.IndustryID,
		Tokens:                    tokens,
	})
}

// ensureBusinessOnboardingCatalogRef validates that a UUID resolves to an active catalog row.
// build must be a query builder that selects at least (id, slug).
func (h *BusinessOnboardingHandler) ensureBusinessOnboardingCatalogRef(
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
