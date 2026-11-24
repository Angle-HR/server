package handler

import "github.com/Angle-HR/server/internal/apidoc"

// Auth OpenAPI types (identity half; split out of the former product_doc_types.go).

// AuthSignupRequest is the product signup request body.
type AuthSignupRequest struct {
	Email    string `json:"email" example:"jerry@example.com"`
	Password string `json:"password" example:"secure-password-1"`
}

// AuthSignupPatchRequest updates email before verification.
type AuthSignupPatchRequest struct {
	VerificationSessionID string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email                 string `json:"email" example:"newemail@example.com"`
}

// AuthSignupData is returned after signup or resend.
type AuthSignupData struct {
	VerificationSessionID    string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email                    string `json:"email" example:"jerry@example.com"`
	CodeExpiresInSeconds     int    `json:"code_expires_in_seconds" example:"300"`
	ResendAvailableInSeconds int    `json:"resend_available_in_seconds" example:"30"`
}

// AuthSignupEnvelope is a successful signup response.
type AuthSignupEnvelope struct {
	Data AuthSignupData `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// AuthVerifyEmailRequest validates a 6-digit OTP.
type AuthVerifyEmailRequest struct {
	VerificationSessionID string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Code                  string `json:"code" example:"224879"`
}

// AuthResendVerificationRequest requests a new OTP.
type AuthResendVerificationRequest struct {
	VerificationSessionID string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// OnboardingProgressSummary is embedded in auth and onboarding responses.
type OnboardingProgressSummary struct {
	Status         string   `json:"status" example:"in_progress" enums:"in_progress,completed"`
	CurrentStep    *string  `json:"current_step,omitempty" example:"profile"`
	CompletedSteps []string `json:"completed_steps" example:"verify_email,profile"`
	NextStep       *string  `json:"next_step,omitempty" example:"compliance"`
}

// AuthSessionUser is the short user summary returned with a new session.
type AuthSessionUser struct {
	ID            string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email         string  `json:"email,omitempty" example:"jerry@example.com"`
	EmailVerified bool    `json:"email_verified" example:"true"`
	AccountType   *string `json:"account_type,omitempty" example:"business"`
	Region        string  `json:"region" example:"uk"`
}

// AuthSessionData is returned after verify-email, login, login/otp/verify, login/totp and accept-invite.
// The session itself travels in the Set-Cookie header; there are no tokens in the body.
type AuthSessionData struct {
	Onboarding *OnboardingProgressSummary `json:"onboarding,omitempty"`
	User       *AuthSessionUser           `json:"user,omitempty"`
	// CSRFToken must be sent as X-CSRF-Token on every POST/PUT/PATCH/DELETE to an authenticated route.
	CSRFToken string `json:"csrf_token" example:"2Yb7vZk0n1l3o9Qm5r8s4tA6uC1dE3fGhIjKlMnOpQw"`
}

// AuthMFARequiredData is returned when password/OTP succeeded but TOTP is required.
// The MFA pre-auth cookie is set; call POST /auth/login/totp next.
type AuthMFARequiredData struct {
	TOTPRequired bool `json:"totp_required" example:"true"`
	ExpiresIn    int  `json:"expires_in" example:"300"`
}

// AuthSessionEnvelope is a successful login-family response.
type AuthSessionEnvelope struct {
	Data AuthSessionData `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// AuthLoginRequest is the login request body.
type AuthLoginRequest struct {
	Email    string `json:"email" example:"jerry@example.com"`
	Password string `json:"password" example:"secure-password-1"`
}

// AuthLoginVerificationRequiredDetails is returned in error.details when login succeeds on
// password but email is unverified.
type AuthLoginVerificationRequiredDetails struct {
	VerificationSessionID    string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email                    string `json:"email" example:"jerry@example.com"`
	CodeExpiresInSeconds     int    `json:"code_expires_in_seconds" example:"300"`
	ResendAvailableInSeconds int    `json:"resend_available_in_seconds" example:"30"`
}

// AuthForgotPasswordRequest starts a password reset.
type AuthForgotPasswordRequest struct {
	Email string `json:"email" example:"jerry@example.com"`
}

// AuthResetPasswordRequest completes a password reset.
type AuthResetPasswordRequest struct {
	Token    string `json:"token" example:"opaque-reset-token"`
	Password string `json:"password" example:"new-secure-password"`
}

// AuthLoginOTPRequest requests a passwordless login code.
type AuthLoginOTPRequest struct {
	Email string `json:"email" example:"jerry@example.com"`
}

// AuthLoginOTPVerifyRequest verifies a passwordless login code.
type AuthLoginOTPVerifyRequest struct {
	VerificationSessionID string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Code                  string `json:"code" example:"224879"`
}

// AuthLoginTOTPRequest completes MFA after password or OTP login.
type AuthLoginTOTPRequest struct {
	Code string `json:"code" example:"123456"`
}

// AuthTOTPConfirmRequest confirms TOTP enrollment.
type AuthTOTPConfirmRequest struct {
	Code string `json:"code" example:"123456"`
}

// AuthTOTPDisableRequest disables TOTP.
type AuthTOTPDisableRequest struct {
	Code     string `json:"code" example:"123456"`
	Password string `json:"password" example:"secure-password-1"`
}

// AuthTOTPEnrollData is returned from enroll.
type AuthTOTPEnrollData struct {
	Secret     string `json:"secret" example:"JBSWY3DPEHPK3PXP"`
	OTPAuthURL string `json:"otpauth_url" example:"otpauth://totp/OpenHR:jerry@example.com?secret=..."`
}

// AuthTOTPEnrollEnvelope wraps enroll data.
type AuthTOTPEnrollEnvelope struct {
	Data AuthTOTPEnrollData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// AuthMeData is the current product user.
type AuthMeData struct {
	ID            string  `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email         string  `json:"email" example:"jerry@example.com"`
	EmailVerified bool    `json:"email_verified" example:"true"`
	AccountType   *string `json:"account_type,omitempty" example:"business"`
	FirstName     *string `json:"first_name,omitempty"`
	LastName      *string `json:"last_name,omitempty"`
	LegalFullName *string `json:"legal_full_name,omitempty"`
	CountryID     *string `json:"country_id,omitempty"`
	Region        string  `json:"region" example:"uk"`
	TOTPEnabled   bool    `json:"totp_enabled" example:"false"`
	// CSRFToken is the session's synchronizer token; clients call /auth/me on page load to obtain it.
	CSRFToken  string                    `json:"csrf_token" example:"2Yb7vZk0n1l3o9Qm5r8s4tA6uC1dE3fGhIjKlMnOpQw"`
	Onboarding OnboardingProgressSummary `json:"onboarding"`
	// Roles are the caller's company roles; empty until they belong to a company.
	Roles []string `json:"roles"`
	// Permissions are everything those roles allow, so a client can show only the actions the caller may take.
	Permissions []string `json:"permissions"`
}

// AuthMeEnvelope wraps /auth/me.
type AuthMeEnvelope struct {
	Data AuthMeData   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// AuthMessageData is a simple status message.
type AuthMessageData struct {
	Message string `json:"message" example:"logged out"`
}

// AuthMessageEnvelope wraps a message response.
type AuthMessageEnvelope struct {
	Data AuthMessageData `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// AuthInviteData is the public invite preview.
type AuthInviteData struct {
	Email            string `json:"email" example:"member@example.com"`
	OrganizationName string `json:"organization_name" example:"ANGLE"`
	ExpiresAt        string `json:"expires_at" example:"2026-08-24T12:00:00Z"`
}

// AuthInviteEnvelope wraps invite preview.
type AuthInviteEnvelope struct {
	Data AuthInviteData `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// AuthAcceptInviteRequest accepts a product org invite.
type AuthAcceptInviteRequest struct {
	Token     string  `json:"token" example:"opaque-invite-token"`
	Password  string  `json:"password" example:"secure-password-1"`
	FirstName *string `json:"first_name,omitempty" example:"Jerry"`
	LastName  *string `json:"last_name,omitempty" example:"Oluwasegun"`
}

// AuthCreateOrgInviteRequest creates an org invite.
type AuthCreateOrgInviteRequest struct {
	Email string `json:"email" example:"member@example.com"`
}

// AuthCreateOrgInviteData is returned after creating an invite.
type AuthCreateOrgInviteData struct {
	Email     string `json:"email" example:"member@example.com"`
	ExpiresAt string `json:"expires_at" example:"2026-08-24T12:00:00Z"`
}

// AuthCreateOrgInviteEnvelope wraps create invite.
type AuthCreateOrgInviteEnvelope struct {
	Data AuthCreateOrgInviteData `json:"data"`
	Meta *apidoc.Meta            `json:"meta,omitempty"`
}

// RegionReissue is returned when an onboarding step moved the account out of the global holding region. The
// server rotated the session: a new cookie is set in the same response and the previous session id is dead, so
// the client must replace its stored CSRF token with this one.
type RegionReissue struct {
	CSRFToken string `json:"csrf_token" example:"2Yb7vZk0n1l3o9Qm5r8s4tA6uC1dE3fGhIjKlMnOpQw"`
	Region    string `json:"region" example:"uk"`
}
