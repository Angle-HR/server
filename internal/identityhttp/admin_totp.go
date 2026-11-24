package identityhttp

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/admin"
	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/session"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/besteffort"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// Admin TOTP guesses are limited per admin (Redis namespace "admin_totp", separate from the product "totp"): five
// wrong codes in five minutes lock the admin's TOTP checks for fifteen minutes.
const (
	adminTOTPLockoutMaxAttempts = 5
	adminTOTPLockoutWindow      = 5 * time.Minute
	adminTOTPLockoutDuration    = 15 * time.Minute
)

// Audit actions written by the admin TOTP flows (admin.audit_logs.action).
const (
	AuditTOTPEnrollStart = "admin.totp.enroll_start"
	AuditTOTPEnroll      = "admin.totp.enroll"
	AuditTOTPVerify      = "admin.totp.verify"
	AuditTOTPFail        = "admin.totp.fail"
	AuditTOTPDisable     = "admin.totp.disable"
	AuditTOTPReset       = "admin.totp.reset"
)

type adminTOTPCodeBody struct {
	Code string `json:"code" validate:"required,len=6,numeric"`
}

type adminTOTPDisableBody struct {
	Code     string `json:"code" validate:"required,len=6,numeric"`
	Password string `json:"password" validate:"required,min=8,max=128"`
}

// UseTOTP wires the authenticator crypto and the ADMIN_TOTP_REQUIRED switch into the handler. Without crypto the
// TOTP routes answer 500 and a TOTP-enabled admin cannot sign in (fail closed).
func (h *AdminHandler) UseTOTP(crypto *auth.TOTPCrypto, required bool) {
	h.TOTPCrypto = crypto
	h.TOTPRequired = required
}

// auditAs writes an audit row for an actor that has no admin session in the request context (login steps).
func (h *AdminHandler) auditAs(r *http.Request, actor uuid.UUID, action, resourceID string, meta map[string]any) {
	if h.Store == nil {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	besteffort.Log(r.Context(), "h.Store.WriteAudit",
		h.Store.WriteAudit(r.Context(), actor, action, "staff", resourceID, meta, r.RemoteAddr))
}

// startPasswordOnlyOrPreAuth finishes a correct password: TOTP admins get the pre-auth cookie and no session; the
// others get a session whose MFA flag is false (which only matters when ADMIN_TOTP_REQUIRED is on).
func (h *AdminHandler) afterPassword(w http.ResponseWriter, r *http.Request, user admin.User) {
	if user.TOTPEnabled() {
		if err := h.Sessions.StartPreAuth(w, r, user.ID, ""); err != nil {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		response.Success(w, r, http.StatusOK, map[string]any{
			"totp_required": true,
			"expires_in":    int(session.PreAuthTTL / time.Second),
		})
		return
	}
	ident, err := h.Sessions.Start(w, r, user.ID, "", false)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	data := map[string]any{
		"user":       map[string]any{"id": user.ID, "email": user.Email},
		"csrf_token": ident.CSRFToken,
	}
	if h.TOTPRequired {
		data["totp_enrollment_required"] = true
	}
	response.Success(w, r, http.StatusOK, data)
}

// checkTOTPCode verifies code against the admin's stored secret under the lockout. On failure it writes the response
// and returns false.
func (h *AdminHandler) checkTOTPCode(w http.ResponseWriter, r *http.Request, user admin.User, code string) bool {
	ctx := r.Context()
	if h.TOTPCrypto == nil || user.TOTPSecret == nil {
		response.Error(w, r, apperror.ErrInternal)
		return false
	}
	if err := h.TOTPLockout.Check(ctx, user.ID.String()); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeTooManyAttempts, apperror.MsgTooManyAttempts))
		return false
	}
	secret, err := h.TOTPCrypto.Decrypt(*user.TOTPSecret)
	if err != nil || !auth.ValidateTOTP(secret, code) {
		locked, lockErr := h.TOTPLockout.RecordFailure(ctx, user.ID.String())
		besteffort.Log(ctx, "h.TOTPLockout.RecordFailure", lockErr)
		h.auditAs(r, user.ID, AuditTOTPFail, user.ID.String(), map[string]any{"locked": locked})
		response.Error(w, r, apperror.New(apperror.CodeInvalidVerificationCode, "invalid authenticator code"))
		return false
	}
	besteffort.Log(ctx, "h.TOTPLockout.Reset", h.TOTPLockout.Reset(ctx, user.ID.String()))
	return true
}

// loginTOTP godoc
//
//	@Summary		Complete admin login with TOTP
//	@Description	Completes login for an admin who has TOTP enabled. Requires the single-use admin pre-auth cookie set by POST /admin/auth/login (valid 5 minutes, separate from the product pre-auth) plus a valid authenticator code, then starts the admin session (MFA satisfied) and returns the CSRF token. A wrong code keeps the pre-auth alive; five wrong codes in five minutes lock the admin's TOTP checks for fifteen (429 too_many_attempts).
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Param			body	body		identityhttp.AuthLoginTOTPRequest	true	"Code"
//	@Success		200		{object}	identityhttp.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/login/totp [post]
func (h *AdminHandler) loginTOTP(w http.ResponseWriter, r *http.Request) {
	var req adminTOTPCodeBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}

	pre, err := h.Sessions.PeekPreAuth(r)
	if err != nil {
		if errors.Is(err, session.ErrUnavailable) {
			response.Error(w, r, apperror.ErrInternal)
			return
		}
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), pre.UserID)
	if err != nil || !user.IsActive || !user.TOTPEnabled() {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if !h.checkTOTPCode(w, r, user, req.Code) {
		return
	}
	// Spend the pre-auth (single use), then issue the full session under a fresh id.
	if _, consumeErr := h.Sessions.ConsumePreAuth(w, r); consumeErr != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	h.auditAs(r, user.ID, AuditTOTPVerify, user.ID.String(), nil)
	h.respondAdminSession(w, r, user.ID, user.Email)
}

// totpEnroll godoc
//
//	@Summary		Start admin TOTP enrolment
//	@Description	Generates a new authenticator secret and otpauth URL for the signed-in admin (stored encrypted, not active until confirmed). Allowed for a session that still has to enrol when ADMIN_TOTP_REQUIRED is on. 409 when TOTP is already enabled.
//	@Tags			admin/auth
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Success		200	{object}	identityhttp.AuthTOTPEnrollEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/totp/enroll [post]
func (h *AdminHandler) totpEnroll(w http.ResponseWriter, r *http.Request) {
	id, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.TOTPCrypto == nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if user.TOTPEnabled() {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}
	key, err := auth.GenerateTOTP(user.Email)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	encrypted, err := h.TOTPCrypto.Encrypt(key.Secret())
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	stored, err := h.Store.SetTOTPSecret(r.Context(), id, encrypted)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if !stored {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}
	h.audit(r, AuditTOTPEnrollStart, "staff", id.String(), nil)
	response.Success(w, r, http.StatusOK, AuthTOTPEnrollData{Secret: key.Secret(), OTPAuthURL: key.URL()})
}

// totpConfirm godoc
//
//	@Summary		Confirm admin TOTP enrolment
//	@Description	Validates a code from the authenticator app and enables TOTP. Every other session of the admin is revoked and the caller gets a fresh session (MFA satisfied) with a new CSRF token in the body.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			body	body		identityhttp.AuthTOTPConfirmRequest	true	"Code"
//	@Success		200		{object}	identityhttp.AdminTokenEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		429		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/totp/confirm [post]
func (h *AdminHandler) totpConfirm(w http.ResponseWriter, r *http.Request) {
	var req adminTOTPCodeBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}
	id, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil || user.TOTPSecret == nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "enroll totp before confirming"))
		return
	}
	if user.TOTPEnabled() {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}
	if !h.checkTOTPCode(w, r, user, req.Code) {
		return
	}
	enabled, err := h.Store.EnableTOTP(r.Context(), id)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if !enabled {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "totp already enabled"))
		return
	}
	h.audit(r, AuditTOTPEnroll, "staff", id.String(), nil)
	// Strengthening: end every session of this admin, then give the caller a fresh one with MFA satisfied.
	if revokeErr := h.Sessions.RevokeAll(r.Context(), id); revokeErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	h.respondAdminSession(w, r, id, user.Email)
}

// totpDisable godoc
//
//	@Summary		Disable own admin TOTP
//	@Description	Disables TOTP for the signed-in admin after checking the password and a current authenticator code, and signs the admin out everywhere. Refused (403) while ADMIN_TOTP_REQUIRED is on: a superadmin can reset another admin instead.
//	@Tags			admin/auth
//	@Accept			json
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			body	body		identityhttp.AuthTOTPDisableRequest	true	"Password and code"
//	@Success		200		{object}	identityhttp.AuthMessageEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/auth/totp/disable [post]
func (h *AdminHandler) totpDisable(w http.ResponseWriter, r *http.Request) {
	var req adminTOTPDisableBody
	if err := decodeJSON(r, &req); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	if err := h.validate.Struct(req); err != nil {
		response.Error(w, r, validationError(err))
		return
	}
	id, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.TOTPRequired {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "totp is required for admins and cannot be disabled"))
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil || !user.TOTPEnabled() {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "totp is not enabled"))
		return
	}
	if user.PasswordHash == nil || !auth.CheckPassword(*user.PasswordHash, req.Password) {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if !h.checkTOTPCode(w, r, user, req.Code) {
		return
	}
	if _, err = h.Store.ClearTOTP(r.Context(), id); err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	h.audit(r, AuditTOTPDisable, "staff", id.String(), map[string]any{"by": "self"})
	if revokeErr := h.Sessions.RevokeAll(r.Context(), id); revokeErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if logoutErr := h.Sessions.Logout(w, r); logoutErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "totp disabled"})
}

// resetStaffTOTP godoc
//
//	@Summary		Reset an admin's TOTP
//	@Description	Superadmin only (role superadmin plus admins:write). Clears the target admin's authenticator, revokes all their sessions and lifts their TOTP lockout, so they enrol again at next login (forced when ADMIN_TOTP_REQUIRED is on). Audited as admin.totp.reset. Resetting yourself is refused: use disable.
//	@Tags			admin/staff
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			id	path		string	true	"Staff id"
//	@Success		200	{object}	identityhttp.AuthMessageEnvelope
//	@Failure		403	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/admin/staff/{id}/totp/reset [post]
func (h *AdminHandler) resetStaffTOTP(w http.ResponseWriter, r *http.Request) {
	target, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid id"))
		return
	}
	actor, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	roles, err := h.Store.ListRolesForUser(r.Context(), actor)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	if !slices.Contains(roles, admin.RoleSuperadmin) {
		response.Error(w, r, apperror.ErrForbidden)
		return
	}
	if target == actor {
		response.Error(w, r, apperror.New(apperror.CodeForbidden, "use disable to remove your own authenticator"))
		return
	}
	if _, err = h.Store.GetUserByID(r.Context(), target); err != nil {
		response.Error(w, r, err)
		return
	}
	had, err := h.Store.ClearTOTP(r.Context(), target)
	if err != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	besteffort.Log(r.Context(), "h.TOTPLockout.Reset", h.TOTPLockout.Reset(r.Context(), target.String()))
	if revokeErr := h.Sessions.RevokeAll(r.Context(), target); revokeErr != nil {
		response.Error(w, r, apperror.ErrInternal)
		return
	}
	h.audit(r, AuditTOTPReset, "staff", target.String(), map[string]any{"had_totp": had})
	response.Success(w, r, http.StatusOK, AuthMessageData{Message: "totp reset"})
}
