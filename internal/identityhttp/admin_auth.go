package identityhttp

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

type adminLoginBody struct {
	Email    string `json:"email" validate:"required,email,max=254"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

type acceptInviteBody struct {
	Token    string `json:"token" validate:"required"`
	Name     string `json:"name" validate:"required,min=1,max=120"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

// login godoc
//
//	@Summary		Admin login
//	@Description	Authenticates an admin user and starts an admin cookie session (separate from the product session) and returns the CSRF token. An admin with TOTP enabled gets no session: the response is {totp_required:true, expires_in:300} plus the single-use admin pre-auth cookie, and POST /admin/auth/login/totp completes the login. When ADMIN_TOTP_REQUIRED is on and the admin has not enrolled, the session is issued with totp_enrollment_required:true and every route except /admin/auth/{me,totp/enroll,totp/confirm} answers 403 TOTP_ENROLLMENT_REQUIRED until they enrol.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AdminLoginRequest	true	"Login payload"
//	@Success		200		{object}	identityhttp.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		500		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/login [post]
func (h *AdminHandler) login(w http.ResponseWriter, r *http.Request) {
	var req adminLoginBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	ctx := r.Context()
	if err := h.PasswordLockout.Check(ctx, req.Email); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts))
		return
	}

	user, err := h.Store.GetUserByEmail(ctx, req.Email)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if !user.IsActive || user.PasswordHash == nil || !auth.CheckPassword(*user.PasswordHash, req.Password) {
		_, lockoutErr := h.PasswordLockout.RecordFailure(ctx, req.Email)
		besteffort.Log(ctx, "h.PasswordLockout.RecordFailure", lockoutErr)
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	besteffort.Log(ctx, "h.PasswordLockout.Reset", h.PasswordLockout.Reset(ctx, req.Email))

	h.afterPassword(w, r, user)
}

// respondAdminSession starts the admin session (rotating any presented one) and writes the response.
func (h *AdminHandler) respondAdminSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, email string) {
	// Callers reach this only after a verified second factor (login/totp, enrolment confirm): MFA flag set.
	ident, err := h.Sessions.Start(w, r, id, "", true)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	response.Success(w, r, http.StatusOK, map[string]any{
		"user":       map[string]any{"id": id, "email": email},
		"csrf_token": ident.CSRFToken,
	})
}

// logout godoc
//
//	@Summary		Admin logout
//	@Description	Revokes the admin session and clears the admin cookies. Idempotent.
//	@Tags			admin/auth
//	@Produce		json
//	@Success		200	{object}	identityhttp.AuthMessageEnvelope
//	@Router			/admin/auth/logout [post]
func (h *AdminHandler) logout(w http.ResponseWriter, r *http.Request) {
	if err := h.Sessions.Logout(w, r); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "logged out"})
}

// refresh godoc
//
//	@Summary		Admin token refresh (removed)
//	@Description	Removed: admin sessions are server-side with a sliding idle timeout. Always returns 410 SESSION_REFRESH_REMOVED.
//	@Tags			admin/auth
//	@Produce		json
//	@Failure		410	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/refresh [post]
func (h *AdminHandler) refresh(w http.ResponseWriter, r *http.Request) {
	response.Error(w, r, apperror.New(apperror.CodeSessionRefreshRemoved,
		"token refresh was removed; sessions are cookie based with a sliding idle timeout"))
}

// me godoc
//
//	@Summary		Admin profile
//	@Description	Returns the authenticated admin profile, roles, and permissions.
//	@Tags			admin/auth
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Success		200	{object}	identityhttp.AdminMeEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/me [get]
func (h *AdminHandler) me(w http.ResponseWriter, r *http.Request) {
	userID, email, perms, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}

	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil {
		if errors.Is(err, apperror.ErrNotFound) {
			response.Error(w, r, apperror.ErrUnauthorized)
			return
		}
		response.Error(w, r, err)
		return
	}

	roles, err := h.Store.ListRolesForUser(r.Context(), userID)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	csrf := ""
	mfa := false
	if ident, found := auth.SessionFromContext(r.Context()); found {
		csrf = ident.CSRFToken
		mfa = ident.MFA
	}

	response.Success(w, r, http.StatusOK, map[string]any{
		"csrf_token":   csrf,
		"id":           user.ID,
		"email":        email,
		"name":         user.Name,
		"is_active":    user.IsActive,
		"totp_enabled": user.TOTPEnabled(),
		// True while ADMIN_TOTP_REQUIRED is on and this session has not passed a second factor: only the TOTP
		// enrolment routes work.
		"totp_enrollment_required": h.TOTPRequired && !mfa,
		"roles":                    roles,
		"permissions":              perms,
	})
}

// getInvite godoc
//
//	@Summary		Preview admin invite
//	@Description	Validates an invite token and returns the invited email and expiry.
//	@Tags			admin/auth
//	@Produce		json
//	@Param			token	path		string	true	"Invite token"
//	@Success		200		{object}	identityhttp.AdminInvitePreviewEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/invite/{token} [get]
func (h *AdminHandler) getInvite(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	preview, err := h.Store.GetInviteByToken(r.Context(), token)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, preview)
}

// acceptInvite godoc
//
//	@Summary		Accept admin invite
//	@Description	Sets name and password for an invited admin and starts an admin cookie session.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AdminAcceptInviteRequest	true	"Accept payload"
//	@Success		200		{object}	identityhttp.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Failure		410		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/accept-invite [post]
func (h *AdminHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var body acceptInviteBody
	if err := decodeJSON(r, &body); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	body.Token = strings.TrimSpace(body.Token)
	body.Name = strings.TrimSpace(body.Name)
	if err := h.validate.Struct(body); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	if err := auth.ValidatePassword(body.Password); err != nil {
		response.Error(w, r, passwordPolicyError("password"))
		return
	}

	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	result, err := h.Store.AcceptInvite(r.Context(), body.Token, body.Name, hash)
	if err != nil {
		response.Error(w, r, err)
		return
	}

	besteffort.Log(r.Context(), "Store.WriteAudit", h.Store.WriteAudit(
		r.Context(),
		result.User.ID,
		"staff.invite_accept",
		"staff",
		result.User.ID.String(),
		nil,
		r.RemoteAddr,
	))

	h.afterPassword(w, r, result.User)
}
