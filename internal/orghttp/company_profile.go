package orghttp

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Limits of the public profile. The database checks the same bounds (000025_org_public_profile.sql).
const (
	profileNameMax  = 100
	profileAboutMax = 1500
	profileURLMax   = 300
	profileLogoMax  = 500
)

// ProfileData is the company's public profile as the settings page edits it.
type ProfileData struct {
	// Slug is the careers page address: /company/<slug>. Empty until the owner chooses one.
	Slug string `json:"slug"`
	// SuggestedSlug is offered while Slug is empty.
	SuggestedSlug string `json:"suggested_slug,omitempty"`
	DisplayName   string `json:"display_name"`
	// LegalName is shown on the careers page when DisplayName is empty.
	LegalName string `json:"legal_name"`
	About     string `json:"about"`
	Website   string `json:"website"`
	LogoURL   string `json:"logo_url"`
}

// ProfileEnvelope wraps ProfileData.
type ProfileEnvelope struct {
	Data ProfileData `json:"data"`
}

// ProfileInput replaces the public profile. A field left empty is cleared.
type ProfileInput struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	About       string `json:"about"`
	Website     string `json:"website"`
	LogoURL     string `json:"logo_url"`
}

// SlugCheckData says whether the company can take a careers page address.
type SlugCheckData struct {
	Slug      string `json:"slug"`
	Available bool   `json:"available"`
	// Reason is why not, when Available is false.
	Reason string `json:"reason,omitempty"`
}

// SlugCheckEnvelope wraps SlugCheckData.
type SlugCheckEnvelope struct {
	Data SlugCheckData `json:"data"`
}

func profileData(p *orgstore.Profile) ProfileData {
	out := ProfileData{
		Slug: p.Slug, DisplayName: p.DisplayName, LegalName: p.LegalName, About: p.About, Website: p.Website, LogoURL: p.LogoURL,
	}
	if p.Slug == "" {
		out.SuggestedSlug = orgstore.SuggestSlug(p.Name())
	}
	return out
}

// httpsURL reports whether s is an absolute https URL with a host and no credentials.
func httpsURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// validateProfile returns the field errors of an input, with the slug normalized.
func validateProfile(in *ProfileInput) []fieldError {
	in.Slug = orgstore.NormalizeSlug(in.Slug)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	in.About = strings.TrimSpace(in.About)
	in.Website = strings.TrimSpace(in.Website)
	in.LogoURL = strings.TrimSpace(in.LogoURL)
	var errs []fieldError
	if in.Slug != "" {
		if why := orgstore.ValidateSlug(in.Slug); why != "" {
			errs = append(errs, fieldError{Path: "slug", Message: why})
		}
	}
	if utf8.RuneCountInString(in.DisplayName) > profileNameMax {
		errs = append(errs, fieldError{Path: "display_name", Message: "keep this under 100 characters"})
	}
	if utf8.RuneCountInString(in.About) > profileAboutMax {
		errs = append(errs, fieldError{Path: "about", Message: "keep this under 1,500 characters"})
	}
	if in.Website != "" && (utf8.RuneCountInString(in.Website) > profileURLMax || !httpsURL(in.Website)) {
		errs = append(errs, fieldError{Path: "website", Message: "enter a web address that starts with https://"})
	}
	if in.LogoURL != "" && (utf8.RuneCountInString(in.LogoURL) > profileLogoMax || !httpsURL(in.LogoURL)) {
		errs = append(errs, fieldError{Path: "logo_url", Message: "enter an image address that starts with https://"})
	}
	return errs
}

// getProfile godoc
//
//	@Summary		Show the company's public profile
//	@Description	The careers page address, name, about text, website and logo. Anyone in the company can read it.
//	@Tags			organization
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	orghttp.ProfileEnvelope
//	@Router			/organization/profile [get]
func (h *CompanyHandler) getProfile(w http.ResponseWriter, r *http.Request) {
	m, reg, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	p, err := h.Store.Profile(r.Context(), reg, m.CompanyID)
	if errors.Is(err, orgstore.ErrNotFound) {
		err = apperror.New(apperror.CodeNotFound, "not found")
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, profileData(p))
}

// putProfile godoc
//
//	@Summary		Change the company's public profile
//	@Description	Replaces the profile; a field left empty is cleared. The slug is the careers page address and is unique across all companies. Founder and HR 1.
//	@Tags			organization
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.ProfileInput	true	"Profile"
//	@Success		200		{object}	orghttp.ProfileEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/profile [put]
func (h *CompanyHandler) putProfile(w http.ResponseWriter, r *http.Request) {
	m, reg, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	if !m.Perms.Has(rbac.OrgProfileEdit) {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "you cannot change the company's public profile"))
		return
	}
	var in ProfileInput
	if err := decodeStrictBody(w, r, &in); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if errs := validateProfile(&in); len(errs) > 0 {
		response.Error(w, r, apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
			map[string]any{"fields": errs}))
		return
	}
	cur, err := h.Store.Profile(r.Context(), reg, m.CompanyID)
	if err == nil && cur.Slug != "" && in.Slug == "" {
		// A published careers page address is never taken away: links to it are out in the world.
		err = fieldInvalid("slug", "the careers page address cannot be removed once chosen; change it to another instead")
	}
	if err == nil {
		err = h.Store.SetProfile(r.Context(), reg, m.CompanyID, orgstore.Profile{
			Slug: in.Slug, DisplayName: in.DisplayName, About: in.About, Website: in.Website, LogoURL: in.LogoURL,
		})
	}
	switch {
	case errors.Is(err, orgstore.ErrSlugTaken):
		err = apperror.NewWithDetails(apperror.CodeConflict, "this address is already used by another company",
			map[string]any{"fields": []fieldError{{Path: "slug", Message: "this address is already used by another company"}}})
	case errors.Is(err, orgstore.ErrNotFound):
		err = apperror.New(apperror.CodeNotFound, "not found")
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	p, err := h.Store.Profile(r.Context(), reg, m.CompanyID)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, profileData(p))
}

// checkSlug godoc
//
//	@Summary		Check a careers page address
//	@Description	Says whether the company can take the address: well formed, not reserved and not used by another company.
//	@Tags			organization
//	@Produce		json
//	@Security		SessionCookie
//	@Param			slug	query		string	true	"Address to check"
//	@Success		200		{object}	orghttp.SlugCheckEnvelope
//	@Router			/organization/profile/slug-check [get]
func (h *CompanyHandler) checkSlug(w http.ResponseWriter, r *http.Request) {
	m, reg, _, ok := h.scope(w, r)
	if !ok {
		return
	}
	if !m.Perms.Has(rbac.OrgProfileEdit) {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "you cannot change the company's public profile"))
		return
	}
	_ = reg
	slug := orgstore.NormalizeSlug(r.URL.Query().Get("slug"))
	okSlug, why, err := h.Store.SlugAvailable(r.Context(), m.CompanyID, slug)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, SlugCheckData{Slug: slug, Available: okSlug, Reason: why})
}
