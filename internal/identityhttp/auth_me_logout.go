package identityhttp

import (
	"net/http"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// me godoc
//
//	@Summary		Current product user
//	@Description	Returns the authenticated product user, onboarding progress, and the caller's company roles and permissions (empty until they belong to a company).
//	@Tags			auth
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	identityhttp.AuthMeEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/auth/me [get]
func (h *AuthHandler) me(w http.ResponseWriter, r *http.Request) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	ctx := r.Context()
	user, err := h.loadUserByID(ctx, reg, userID)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	summary, err := h.loadOnboardingSummary(ctx, reg, user)
	if err != nil {
		response.Error(w, r, summaryError(err))
		return
	}

	var countryID *string
	if user.CountryID != nil {
		s := user.CountryID.String()
		countryID = &s
	}

	roles, perms := []string{}, []string{}
	if m := h.OrgReads.membership(); m != nil {
		rs, ps, merr := m.Membership(ctx, user.ID.String(), reg)
		if merr != nil {
			besteffort.Log(ctx, "org membership", merr)
			response.Error(w, r, errUnavailable)
			return
		}
		roles, perms = append(roles, rs...), append(perms, ps...)
	} else if h.MeAccess != nil {
		if rs, ps := h.MeAccess(ctx, user.ID.String(), reg); rs != nil || ps != nil {
			roles, perms = append(roles, rs...), append(perms, ps...)
		}
	}

	csrf := ""
	if ident, found := auth.SessionFromContext(ctx); found {
		csrf = ident.CSRFToken
	}

	response.Success(w, r, http.StatusOK, AuthMeData{
		CSRFToken:     csrf,
		ID:            user.ID.String(),
		Email:         user.Email,
		EmailVerified: user.EmailVerifiedAt != nil,
		AccountType:   user.AccountType,
		FirstName:     user.FirstName,
		LastName:      user.LastName,
		LegalFullName: user.LegalFullName,
		CountryID:     countryID,
		Region:        string(reg),
		TOTPEnabled:   h.userTOTPEnabled(user),
		Onboarding:    summary,
		Roles:         roles,
		Permissions:   perms,
	})
}

// logout godoc
//
//	@Summary		Logout
//	@Description	Revokes the caller's session and clears the session cookies. Idempotent: succeeds without a session.
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	identityhttp.AuthMessageEnvelope
//	@Failure		500	{object}	apidoc.ErrorEnvelope
//	@Router			/auth/logout [post]
func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	if h.Sessions != nil {
		if err := h.Sessions.Logout(w, r); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
	}
	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "logged out"})
}
