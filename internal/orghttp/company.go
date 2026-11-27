package orghttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// CurrentDPAVersion is the version of the terms and data processing agreement that counts as current. It is the
// same value as hiring's draft.CurrentDPAVersion (a test keeps them equal) until hiring stops serving the
// agreements routes.
const CurrentDPAVersion = "2026-10-draft"

const agreementKindTermsDPA = "terms_dpa"

// CompanyStore is what the agreements routes need from the data layer. *orgstore.Store implements it.
type CompanyStore interface {
	Resolve(ctx context.Context, reg region.Region, userID string) (*orgstore.Membership, error)
	Readiness(ctx context.Context, reg region.Region, id, dpaVersion string) (*orgstore.Readiness, error)
	Extras(ctx context.Context, reg region.Region, id string) (orgstore.SetupExtras, error)
	AcceptAgreement(ctx context.Context, reg region.Region, companyID, userID, kind, version string) (time.Time, error)
	SetPrivacyContact(ctx context.Context, reg region.Region, companyID, email, dpo string) error
	Profile(ctx context.Context, reg region.Region, companyID string) (*orgstore.Profile, error)
	SetProfile(ctx context.Context, reg region.Region, companyID string, p orgstore.Profile) error
	SlugAvailable(ctx context.Context, companyID, slug string) (bool, string, error)
}

var _ CompanyStore = (*orgstore.Store)(nil)

// Identify reads the signed-in user from the request context.
type Identify func(ctx context.Context) (userID string, reg region.Region, ok bool)

// CompanyHandler serves the company's agreements and privacy contact.
type CompanyHandler struct {
	Store    CompanyStore
	Identify Identify
}

// RegisterProtectedRoutes mounts the routes; the caller applies authentication.
func (h *CompanyHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Get("/organization/agreements", h.companySetup)
	r.Post("/organization/agreements", h.acceptAgreement)
	r.Put("/organization/privacy-contact", h.putPrivacyContact)
	r.Get("/organization/profile", h.getProfile)
	r.Put("/organization/profile", h.putProfile)
	r.Get("/organization/profile/slug-check", h.checkSlug)
}

// CompanySetup is the company's publish prerequisites as the settings page shows them (the same shape hiring
// serves from the same paths).
type CompanySetup struct {
	KYBStatus         string `json:"kyb_status"`
	DPAVersion        string `json:"dpa_version"`
	DPAAccepted       bool   `json:"dpa_accepted"`
	DPAAcceptedAt     string `json:"dpa_accepted_at,omitempty"`
	PrivacyContact    string `json:"privacy_contact_email"`
	DPOContact        string `json:"dpo_contact"`
	DPIAVersion       int    `json:"dpia_version"`
	DPIARecordedAt    string `json:"dpia_recorded_at,omitempty"`
	AutomatedScreenOn bool   `json:"automated_screening_enabled"`
}

// CompanySetupEnvelope wraps CompanySetup.
type CompanySetupEnvelope struct {
	Data CompanySetup `json:"data"`
}

// AcceptRequest accepts a version of the terms and DPA.
type AcceptRequest struct {
	Version string `json:"version"`
}

// PrivacyContactInput is the contact applicants are told to write to.
type PrivacyContactInput struct {
	Email string `json:"privacy_contact_email"`
	DPO   string `json:"dpo_contact"`
}

func (h *CompanyHandler) scope(w http.ResponseWriter, r *http.Request) (*orgstore.Membership, region.Region, string, bool) {
	userID, reg, ok := h.Identify(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return nil, "", "", false
	}
	if reg == region.RegionGlobal {
		response.Error(w, r, apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete))
		return nil, "", "", false
	}
	m, err := h.Store.Resolve(r.Context(), reg, userID)
	if errors.Is(err, orgstore.ErrNoCompany) {
		err = apperror.New(apperror.CodeNotFound, "no organization for this account")
	}
	if err != nil {
		response.Error(w, r, err)
		return nil, "", "", false
	}
	return m, reg, userID, true
}

func (h *CompanyHandler) setup(ctx context.Context, reg region.Region, companyID string) (*CompanySetup, error) {
	rd, err := h.Store.Readiness(ctx, reg, companyID, CurrentDPAVersion)
	if err != nil {
		return nil, err
	}
	x, err := h.Store.Extras(ctx, reg, companyID)
	if err != nil {
		return nil, err
	}
	out := &CompanySetup{
		KYBStatus: rd.KYBStatus, DPAVersion: CurrentDPAVersion, DPAAccepted: rd.DPAAccepted,
		PrivacyContact: rd.PrivacyEmail, DPOContact: rd.DPOContact,
		DPIAVersion: x.DPIAVersion, DPIARecordedAt: x.DPIARecordedAt, AutomatedScreenOn: x.AutomatedScreenOn,
	}
	if rd.DPAAcceptedA != nil {
		out.DPAAcceptedAt = rd.DPAAcceptedA.UTC().Format(time.RFC3339)
	}
	return out, nil
}

func (h *CompanyHandler) respondSetup(w http.ResponseWriter, r *http.Request, reg region.Region, companyID string) {
	out, err := h.setup(r.Context(), reg, companyID)
	if errors.Is(err, orgstore.ErrNotFound) {
		err = apperror.New(apperror.CodeNotFound, "not found")
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, out)
}

// companySetup godoc
//
//	@Summary		Show the company's publishing requirements
//	@Description	Verification status, the DPA version to accept and whether it is accepted, the privacy contact and the DPIA.
//	@Tags			organization
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	orghttp.CompanySetupEnvelope
//	@Router			/organization/agreements [get]
func (h *CompanyHandler) companySetup(w http.ResponseWriter, r *http.Request) {
	m, reg, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	// The same permissions hiring checks: anyone who can create or see jobs, accept agreements or run screening.
	if !m.Perms.Has(rbac.JobCreate) && !m.Perms.Has(rbac.JobViewAll) && !m.Perms.Has(rbac.AccountAgreementsAccept) &&
		!m.Perms.Has(rbac.FormAutomatedScreening) {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "you cannot see the company's publishing requirements"))
		return
	}
	h.respondSetup(w, r, reg, m.CompanyID)
}

// acceptAgreement godoc
//
//	@Summary		Accept the terms and data processing agreement
//	@Description	Only a Founder or Legal can. The version must be the current one shown by GET.
//	@Tags			organization
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.AcceptRequest	true	"Version"
//	@Success		200		{object}	orghttp.CompanySetupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/agreements [post]
func (h *CompanyHandler) acceptAgreement(w http.ResponseWriter, r *http.Request) {
	m, reg, userID, ok := h.scope(w, r)
	if !ok {
		return
	}
	if !m.Perms.Has(rbac.AccountAgreementsAccept) {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "only a Founder or Legal can accept the data processing agreement"))
		return
	}
	var req AcceptRequest
	if err := decodeStrictBody(w, r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if req.Version != CurrentDPAVersion {
		response.Error(w, r, fieldInvalid("version", "this is not the current version; reload and read it again"))
		return
	}
	if _, err := h.Store.AcceptAgreement(r.Context(), reg, m.CompanyID, userID, agreementKindTermsDPA, req.Version); err != nil {
		response.Error(w, r, err)
		return
	}
	h.respondSetup(w, r, reg, m.CompanyID)
}

// putPrivacyContact godoc
//
//	@Summary		Set the privacy contact
//	@Description	The address applicants are told to write to, and an optional DPO contact. Needed before publishing.
//	@Tags			organization
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.PrivacyContactInput	true	"Contact"
//	@Success		200		{object}	orghttp.CompanySetupEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/privacy-contact [put]
func (h *CompanyHandler) putPrivacyContact(w http.ResponseWriter, r *http.Request) {
	m, reg, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	if !m.Perms.Has(rbac.FormPrivacyNotice) {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "you cannot change the company's privacy contact"))
		return
	}
	var in PrivacyContactInput
	if err := decodeStrictBody(w, r, &in); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	email, dpo := strings.TrimSpace(in.Email), strings.TrimSpace(in.DPO)
	var errs []fieldError
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email || utf8.RuneCountInString(email) > 254 {
		errs = append(errs, fieldError{Path: "privacy_contact_email", Message: "enter an email address applicants can write to"})
	}
	if utf8.RuneCountInString(dpo) > 300 {
		errs = append(errs, fieldError{Path: "dpo_contact", Message: "keep this under 300 characters"})
	}
	if len(errs) > 0 {
		response.Error(w, r, apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
			map[string]any{"fields": errs}))
		return
	}
	if err := h.Store.SetPrivacyContact(r.Context(), reg, m.CompanyID, email, dpo); err != nil {
		if errors.Is(err, orgstore.ErrNotFound) {
			err = apperror.New(apperror.CodeNotFound, "not found")
		}
		response.Error(w, r, err)
		return
	}
	h.respondSetup(w, r, reg, m.CompanyID)
}

const companyMaxBodyBytes = 1 << 20

func decodeStrictBody(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, companyMaxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func fieldInvalid(path, msg string) error {
	return apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
		map[string]any{"fields": []fieldError{{Path: path, Message: msg}}})
}
