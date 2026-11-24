package orghttp

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
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// BusinessOnboardingHandler handles the business onboarding endpoint.
type BusinessOnboardingHandler struct {
	// Mover moves a still-global account into its region; nil runs the inline migration (REGION_MOVE).
	Mover    RegionMover
	Router   *dbrouter.DBRouter
	GlobalDB globalDB
	Sessions *session.Manager
	validate *validator.Validate
}

// NewBusinessOnboardingHandler returns a business onboarding handler. Sessions
// is used to rotate the session when this submission migrates the account out of
// the global holding region (see NewProductOnboardingHandler).
func NewBusinessOnboardingHandler(
	router *dbrouter.DBRouter,
	globalDB globalDB,
	sessions *session.Manager,
) *BusinessOnboardingHandler {
	return &BusinessOnboardingHandler{
		Router:   router,
		GlobalDB: globalDB,
		Sessions: sessions,
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
//	@Security		SessionCookie
//	@Param			body	body		orghttp.BusinessRequest	true	"Business onboarding payload"
//	@Success		201		{object}	orghttp.BusinessEnvelope
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
		response.Error(w, r, apperror.InvalidBody(err))
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

	if err := h.ensureBusinessRefsExist(ctx, countryID, companyRoleID, businessTypeID, industryID); err != nil {
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
	ids := businessOnboardingIDs{countryID, companyRoleID, businessTypeID, industryID}
	if err = persistBusinessOnboarding(ctx, pool, userID, &req, ids); err != nil {
		response.Error(w, r, err)
		return
	}

	// The account just moved regions: rotate the session so the new id (and CSRF token) carries the new region.
	var rotated *RegionReissue
	if originalReg != reg {
		ident, rotateErr := h.Sessions.Rotate(w, r, string(reg))
		if rotateErr != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		rotated = &RegionReissue{CSRFToken: ident.CSRFToken, Region: string(reg)}
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
		Session:                   rotated,
	})
}

// businessOnboardingIDs are the parsed catalog references of a business submission.
type businessOnboardingIDs struct {
	country, companyRole, businessType, industry uuid.UUID
}

// ensureBusinessRefsExist checks that every referenced catalog entry exists.
func (h *BusinessOnboardingHandler) ensureBusinessRefsExist(
	ctx context.Context,
	countryID, companyRoleID, businessTypeID, industryID uuid.UUID,
) error {
	refs := []struct {
		id    uuid.UUID
		build func(uuid.UUID) (string, []any, error)
		msg   string
	}{
		{countryID, query.LookupCountryByID, apperror.MsgInvalidCountryID},
		{companyRoleID, query.CompanyRoleByID, "unknown company_role_id reference"},
		{businessTypeID, query.BusinessTypeByID, "unknown business_type_id reference"},
		{industryID, query.OnboardingIndustryByID, "unknown industry_id reference"},
	}
	for _, ref := range refs {
		if err := h.ensureBusinessOnboardingCatalogRef(ctx, ref.id, ref.build, ref.msg); err != nil {
			return err
		}
	}
	return nil
}

// migrateForCountry moves a still-global account into the region of the given country.
func (h *BusinessOnboardingHandler) migrateForCountry(
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
	newReg, err := moveUser(ctx, h.Mover, h.Router, h.GlobalDB, userID, region.Region(target))
	if err != nil {
		return region.RegionUnknown, apperror.ErrInternal
	}
	return newReg, nil
}

// persistBusinessOnboarding writes the user profile, organization, catalog
// references and registration details in one transaction.
func persistBusinessOnboarding(
	ctx context.Context,
	pool dataPool,
	userID uuid.UUID,
	req *businessOnboardingRequest,
	ids businessOnboardingIDs,
) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return apperror.ErrInternal
	}
	defer rollbackOnError(ctx, tx)

	// Persist business profile fields on the user row.
	userSQL, userArgs, err := query.UpdateAccountUserBusinessProfile(userID, req.LegalFullName)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, userSQL, userArgs...); execErr != nil {
		return apperror.ErrInternal
	}

	// Create or update the organization row owned by this user.
	orgSQL, orgArgs, err := query.UpsertOrganizationProfile(userID, req.LegalBusinessName, ids.companyRole)
	if err != nil {
		return apperror.ErrInternal
	}
	if _, execErr := tx.Exec(ctx, orgSQL, orgArgs...); execErr != nil {
		return apperror.ErrInternal
	}

	// Save business type and industry on the organization row.
	bizSQL, bizArgs, err := query.UpdateOrganizationCatalog(userID, ids.businessType, ids.industry)
	if err != nil {
		return apperror.ErrInternal
	}
	if scanErr := tx.QueryRow(ctx, bizSQL, bizArgs...).Scan(new(uuid.UUID)); scanErr != nil {
		return apperror.ErrInternal
	}

	// Save country, BIN number, and registered address on the organization row.
	regSQL, regArgs, err := query.UpdateOrganizationRegistration(
		userID, ids.country, req.BINumber, req.BusinessRegisteredAddress,
	)
	if err != nil {
		return apperror.ErrInternal
	}
	if scanErr := tx.QueryRow(ctx, regSQL, regArgs...).Scan(new(uuid.UUID)); scanErr != nil {
		return apperror.ErrInternal
	}

	if commitErr := tx.Commit(ctx); commitErr != nil {
		return apperror.ErrInternal
	}
	return nil
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
