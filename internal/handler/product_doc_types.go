package handler

import "github.com/Angle-HR/server/internal/apidoc"

// Product onboarding OpenAPI types.

// AuthSignupRequest is the product signup request body.
type AuthSignupRequest struct {
	Email    string `json:"email" example:"jerry@example.com"`
	Password string `json:"password" example:"secure-password-here"`
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

// AuthTokenData is returned after verify-email or login.
type AuthTokenData struct {
	AccessToken  string                    `json:"access_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	RefreshToken string                    `json:"refresh_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	ExpiresIn    int                       `json:"expires_in" example:"3600"`
	Onboarding   OnboardingProgressSummary `json:"onboarding"`
}

// AuthMFARequiredData is returned when password/OTP succeeded but TOTP is required.
type AuthMFARequiredData struct {
	TOTPRequired bool   `json:"totp_required" example:"true"`
	MFAToken     string `json:"mfa_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	ExpiresIn    int    `json:"expires_in" example:"300"`
}

// AuthTokenEnvelope is a successful verify-email or login response.
type AuthTokenEnvelope struct {
	Data AuthTokenData `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

// AuthLoginRequest is the login request body.
type AuthLoginRequest struct {
	Email    string `json:"email" example:"jerry@example.com"`
	Password string `json:"password" example:"secure-password-here"`
}

// AuthLoginVerificationRequiredDetails is returned in error.details when login succeeds on password but email is unverified.
type AuthLoginVerificationRequiredDetails struct {
	VerificationSessionID    string `json:"verification_session_id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email                    string `json:"email" example:"jerry@example.com"`
	CodeExpiresInSeconds     int    `json:"code_expires_in_seconds" example:"300"`
	ResendAvailableInSeconds int    `json:"resend_available_in_seconds" example:"30"`
}

// AuthRefreshRequest exchanges a refresh token.
type AuthRefreshRequest struct {
	RefreshToken string `json:"refresh_token" example:"eyJhbGciOiJIUzI1NiIs..."`
}

// AuthRefreshData is returned after token refresh.
type AuthRefreshData struct {
	AccessToken string `json:"access_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	ExpiresIn   int    `json:"expires_in" example:"3600"`
}

// AuthRefreshEnvelope is a successful refresh response.
type AuthRefreshEnvelope struct {
	Data AuthRefreshData `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// AuthLogoutRequest revokes a refresh token.
type AuthLogoutRequest struct {
	RefreshToken string `json:"refresh_token" example:"eyJhbGciOiJIUzI1NiIs..."`
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
	MFAToken string `json:"mfa_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	Code     string `json:"code" example:"123456"`
}

// AuthTOTPConfirmRequest confirms TOTP enrollment.
type AuthTOTPConfirmRequest struct {
	Code string `json:"code" example:"123456"`
}

// AuthTOTPDisableRequest disables TOTP.
type AuthTOTPDisableRequest struct {
	Code     string `json:"code" example:"123456"`
	Password string `json:"password" example:"secure-password-here"`
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
	ID            string                    `json:"id" example:"550e8400-e29b-41d4-a716-446655440000"`
	Email         string                    `json:"email" example:"jerry@example.com"`
	EmailVerified bool                      `json:"email_verified" example:"true"`
	AccountType   *string                   `json:"account_type,omitempty" example:"business"`
	FirstName     *string                   `json:"first_name,omitempty"`
	LastName      *string                   `json:"last_name,omitempty"`
	LegalFullName *string                   `json:"legal_full_name,omitempty"`
	CountryID     *string                   `json:"country_id,omitempty"`
	Region        string                    `json:"region" example:"uk"`
	TOTPEnabled   bool                      `json:"totp_enabled" example:"false"`
	Onboarding    OnboardingProgressSummary `json:"onboarding"`
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
	Password  string  `json:"password" example:"secure-password-here"`
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

// ProductProfileRequest upserts account type and profile fields.
type ProductProfileRequest struct {
	AccountType       string  `json:"account_type" example:"business" enums:"individual,business"`
	FirstName         *string `json:"first_name,omitempty" example:"Jerry"`
	LastName          *string `json:"last_name,omitempty" example:"Oluwasegun"`
	CountryID         *string `json:"country_id,omitempty" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	LegalBusinessName *string `json:"legal_business_name,omitempty" example:"ANGLE"`
	LegalFullName     *string `json:"legal_full_name,omitempty" example:"Jerry Oluwasegun"`
	CompanyRoleID     *string `json:"company_role_id,omitempty" example:"60000000-0000-4000-8000-000000000001"`
}

// ProductProfileData is the profile step response payload.
type ProductProfileData struct {
	AccountType       string                    `json:"account_type" example:"business"`
	FirstName         *string                   `json:"first_name,omitempty"`
	LastName          *string                   `json:"last_name,omitempty"`
	CountryID         *string                   `json:"country_id,omitempty"`
	LegalBusinessName *string                   `json:"legal_business_name,omitempty"`
	LegalFullName     *string                   `json:"legal_full_name,omitempty"`
	CompanyRoleID     *string                   `json:"company_role_id,omitempty"`
	Region            string                    `json:"region" example:"uk"`
	Onboarding        OnboardingProgressSummary `json:"onboarding"`
}

// ProductProfileEnvelope is a successful profile upsert response.
type ProductProfileEnvelope struct {
	Data ProductProfileData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// ProductAddressRequest upserts workspace address.
type ProductAddressRequest struct {
	CountryID        string            `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	EntryMode        string            `json:"entry_mode" example:"manual" enums:"search,manual"`
	Line1            string            `json:"line_1" example:"10 Downing Street"`
	Line2            *string           `json:"line_2,omitempty"`
	City             string            `json:"city" example:"London"`
	StateOrCounty    string            `json:"state_or_county" example:"Greater London"`
	PostCode         string            `json:"post_code" example:"SW1A 2AA"`
	FormattedAddress *string           `json:"formatted_address,omitempty"`
	Identification   map[string]string `json:"identification,omitempty" swaggertype:"object,string"`
}

// ProductAddressData is the address step response payload.
type ProductAddressData struct {
	CountryID          string                    `json:"country_id"`
	EntryMode          string                    `json:"entry_mode"`
	Line1              string                    `json:"line_1"`
	Line2              *string                   `json:"line_2,omitempty"`
	City               string                    `json:"city"`
	StateOrCounty      string                    `json:"state_or_county"`
	PostCode           string                    `json:"post_code"`
	FormattedAddress   *string                   `json:"formatted_address,omitempty"`
	Identification     map[string]string         `json:"identification,omitempty" swaggertype:"object,string"`
	VerificationStatus string                    `json:"verification_status" example:"unverified" enums:"unverified,verified,failed"`
	Onboarding         OnboardingProgressSummary `json:"onboarding"`
}

// ProductAddressEnvelope is a successful address upsert response.
type ProductAddressEnvelope struct {
	Data ProductAddressData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// VerifyAddressRequest is the address verification request body.
type VerifyAddressRequest struct {
	CountryID        string  `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	EntryMode        string  `json:"entry_mode" example:"search" enums:"search,manual"`
	Line1            string  `json:"line_1" example:"10 Downing Street"`
	Line2            *string `json:"line_2,omitempty"`
	City             string  `json:"city" example:"London"`
	StateOrCounty    string  `json:"state_or_county" example:"Greater London"`
	PostCode         string  `json:"post_code" example:"SW1A 2AA"`
	FormattedAddress *string `json:"formatted_address,omitempty"`
	PlaceID          *string `json:"place_id,omitempty" example:"ChIJ..."`
}

// VerifyAddressResponse is the address verification response payload.
type VerifyAddressResponse struct {
	CountryID          string  `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	EntryMode          string  `json:"entry_mode" example:"search" enums:"search,manual"`
	Line1              string  `json:"line_1" example:"10 Downing Street"`
	Line2              *string `json:"line_2,omitempty"`
	City               string  `json:"city" example:"London"`
	StateOrCounty      string  `json:"state_or_county" example:"Greater London"`
	PostCode           string  `json:"post_code" example:"SW1A 2AA"`
	FormattedAddress   *string `json:"formatted_address,omitempty"`
	VerificationStatus string  `json:"verification_status" example:"verified" enums:"verified,failed,unverified"`
	FailureReason      *string `json:"failure_reason,omitempty" example:"not_verifiable" enums:"not_verifiable,invalid_address"`
}

// VerifyAddressEnvelope is a successful address verification response.
type VerifyAddressEnvelope struct {
	Data VerifyAddressResponse `json:"data"`
	Meta *apidoc.Meta          `json:"meta,omitempty"`
}

// ProductBusinessRequest upserts compliance fields (business type, industry, employee count).
type ProductBusinessRequest struct {
	BusinessTypeID string `json:"business_type_id" example:"61000000-0000-4000-8000-000000000001"`
	IndustryID     string `json:"industry_id" example:"62000000-0000-4000-8000-000000000001"`
	EmployeeCount  int    `json:"employee_count" example:"25"`
}

// ProductComplianceRequest is an alias for the compliance step request body.
type ProductComplianceRequest = ProductBusinessRequest

// ProductBusinessData is the compliance step response payload.
type ProductBusinessData struct {
	BusinessTypeID string                    `json:"business_type_id"`
	IndustryID     string                    `json:"industry_id"`
	EmployeeCount  int                       `json:"employee_count"`
	Onboarding     OnboardingProgressSummary `json:"onboarding"`
}

// ProductComplianceData is an alias for the compliance step response payload.
type ProductComplianceData = ProductBusinessData

// ProductComplianceEnvelope is a successful compliance upsert response.
type ProductComplianceEnvelope struct {
	Data ProductComplianceData `json:"data"`
	Meta *apidoc.Meta          `json:"meta,omitempty"`
}

// ProductBusinessEnvelope is a successful business upsert response.
type ProductBusinessEnvelope struct {
	Data ProductBusinessData `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// ProductProfileState is saved profile fields in status response.
type ProductProfileState struct {
	AccountType       string  `json:"account_type"`
	FirstName         *string `json:"first_name,omitempty"`
	LastName          *string `json:"last_name,omitempty"`
	CountryID         *string `json:"country_id,omitempty"`
	LegalBusinessName *string `json:"legal_business_name,omitempty"`
	LegalFullName     *string `json:"legal_full_name,omitempty"`
	CompanyRoleID     *string `json:"company_role_id,omitempty"`
}

// ProductAddressState is saved address fields in status response.
type ProductAddressState struct {
	CountryID          string            `json:"country_id"`
	EntryMode          string            `json:"entry_mode"`
	Line1              string            `json:"line_1"`
	Line2              *string           `json:"line_2,omitempty"`
	City               string            `json:"city"`
	StateOrCounty      string            `json:"state_or_county"`
	PostCode           string            `json:"post_code"`
	FormattedAddress   *string           `json:"formatted_address,omitempty"`
	Identification     map[string]string `json:"identification,omitempty" swaggertype:"object,string"`
	VerificationStatus string            `json:"verification_status"`
}

// ProductBusinessState is saved compliance fields in status response.
type ProductBusinessState struct {
	BusinessTypeID string `json:"business_type_id"`
	IndustryID     string `json:"industry_id"`
	EmployeeCount  int    `json:"employee_count"`
}

// ProductComplianceState is an alias for saved compliance fields in status response.
type ProductComplianceState = ProductBusinessState

// AddressSearchRequest is the address autocomplete request body.
type AddressSearchRequest struct {
	Query     string `json:"query" example:"10 Downing"`
	CountryID string `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
}

// AddressSuggestion is one address autocomplete candidate.
type AddressSuggestion struct {
	PlaceID          string  `json:"place_id" example:"ChIJ..."`
	Description      string  `json:"description" example:"10 Downing Street, London, UK"`
	Line1            string  `json:"line_1" example:"10 Downing Street"`
	Line2            *string `json:"line_2,omitempty"`
	City             string  `json:"city" example:"London"`
	StateOrCounty    string  `json:"state_or_county" example:"Greater London"`
	PostCode         string  `json:"post_code" example:"SW1A 2AA"`
	FormattedAddress string  `json:"formatted_address" example:"10 Downing Street, London SW1A 2AA, UK"`
}

// AddressSearchData is the address search response payload.
type AddressSearchData struct {
	Suggestions []AddressSuggestion `json:"suggestions"`
}

// AddressSearchEnvelope is a successful address search response.
type AddressSearchEnvelope struct {
	Data AddressSearchData `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// IdentificationRequirementField describes one business identification input.
type IdentificationRequirementField struct {
	Key         string `json:"key" example:"registration_number"`
	Label       string `json:"label" example:"Company Registration Number (CRN)"`
	FormatHint  string `json:"format_hint" example:"8 digits, or SC/NI prefix + 6 digits"`
	Placeholder string `json:"placeholder" example:"12345678"`
	Pattern     string `json:"pattern" example:"^(\\d{8}|(SC|NI)\\d{6})$"`
	Required    bool   `json:"required" example:"true"`
}

// IdentificationRequirementsData is the identification requirements payload.
type IdentificationRequirementsData struct {
	CountryID   string                           `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	CountrySlug string                           `json:"country_slug" example:"united-kingdom"`
	Fields      []IdentificationRequirementField `json:"fields"`
}

// IdentificationRequirementsEnvelope is a successful identification requirements response.
type IdentificationRequirementsEnvelope struct {
	Data IdentificationRequirementsData `json:"data"`
	Meta *apidoc.Meta                   `json:"meta,omitempty"`
}

// ProductOnboardingStatusData is the full onboarding state.
type ProductOnboardingStatusData struct {
	Status         string                `json:"status" example:"in_progress" enums:"in_progress,completed"`
	AccountType    *string               `json:"account_type,omitempty" example:"business"`
	CurrentStep    *string               `json:"current_step,omitempty" example:"identification_address"`
	CompletedSteps []string              `json:"completed_steps"`
	NextStep       *string               `json:"next_step,omitempty" example:"compliance"`
	Profile        *ProductProfileState  `json:"profile,omitempty"`
	Address        *ProductAddressState  `json:"address,omitempty"`
	Compliance     *ProductComplianceState `json:"compliance,omitempty"`
	Business       *ProductBusinessState `json:"business,omitempty"` // deprecated: use compliance
}

// ProductOnboardingStatusEnvelope is a successful status response.
type ProductOnboardingStatusEnvelope struct {
	Data ProductOnboardingStatusData `json:"data"`
	Meta *apidoc.Meta                `json:"meta,omitempty"`
}

// ProductWorkspaceStub is created on onboarding completion.
type ProductWorkspaceStub struct {
	ID   string `json:"id" example:"770e8400-e29b-41d4-a716-446655440000"`
	Slug string `json:"slug" example:"angle"`
}

// ProductOnboardingCompleteData is returned when onboarding finishes.
type ProductOnboardingCompleteData struct {
	Status      string               `json:"status" example:"completed"`
	Workspace   ProductWorkspaceStub `json:"workspace"`
	RedirectURL string               `json:"redirect_url" example:"/dashboard"`
}

// ProductOnboardingCompleteEnvelope is a successful complete response.
type ProductOnboardingCompleteEnvelope struct {
	Data ProductOnboardingCompleteData `json:"data"`
	Meta *apidoc.Meta                  `json:"meta,omitempty"`
}

// BusinessType is a product onboarding business type option.
type BusinessType struct {
	ID   string `json:"id" example:"61000000-0000-4000-8000-000000000001"`
	Name string `json:"name" example:"Early-stage startup"`
	Slug string `json:"slug" example:"early-stage-startup"`
}

// BusinessTypeListEnvelope is a business types catalog response.
type BusinessTypeListEnvelope struct {
	Data []BusinessType `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// OnboardingIndustry is a product onboarding industry option.
type OnboardingIndustry struct {
	ID    string  `json:"id" example:"62000000-0000-4000-8000-000000000001"`
	Name  string  `json:"name" example:"Tech / Software"`
	Slug  string  `json:"slug" example:"tech-software"`
	Emoji *string `json:"emoji,omitempty" example:"💻"`
}

// OnboardingIndustryListEnvelope is an onboarding industries catalog response.
type OnboardingIndustryListEnvelope struct {
	Data []OnboardingIndustry `json:"data"`
	Meta *apidoc.Meta         `json:"meta,omitempty"`
}

// CompanyRole is a product onboarding company role option.
type CompanyRole struct {
	ID      string  `json:"id" example:"60000000-0000-4000-8000-000000000001"`
	Name    string  `json:"name" example:"Founder / CEO"`
	Slug    string  `json:"slug" example:"founder-ceo"`
	IconKey *string `json:"icon_key,omitempty" example:"building"`
}

// CompanyRoleListEnvelope is a company roles catalog response.
type CompanyRoleListEnvelope struct {
	Data []CompanyRole `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

// IndividualRequest is the individual onboarding request body.
type IndividualRequest struct {
	FirstName      string `json:"first_name"       example:"Jerry"`
	LastName       string `json:"last_name"        example:"Oluwasegun"`
	CountryID      string `json:"country_id"       example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	BusinessTypeID string `json:"business_type_id" example:"61000000-0000-4000-8000-000000000001"`
	IndustryID     string `json:"industry_id"      example:"62000000-0000-4000-8000-000000000001"`
	NoOfEmployees  int    `json:"no_of_employees"  example:"10"`
}

// IndividualResponse is the individual onboarding response payload.
type IndividualResponse struct {
	UserID         string `json:"user_id"`
	FirstName      string `json:"first_name"`
	LastName       string `json:"last_name"`
	CountryID      string `json:"country_id"`
	BusinessTypeID string `json:"business_type_id"`
	IndustryID     string `json:"industry_id"`
	NoOfEmployees  int    `json:"no_of_employees"`
}

// IndividualEnvelope is a successful individual onboarding response.
type IndividualEnvelope struct {
	Data IndividualResponse `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// BusinessRequest is the business onboarding request body.
type BusinessRequest struct {
	LegalBusinessName         string `json:"legal_business_name"         example:"Oped Technologies Ltd"`
	LegalFullName             string `json:"legal_full_name"             example:"Oped Oped"`
	CountryID                 string `json:"country_id"                  example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	CompanyRoleID             string `json:"company_role_id"             example:"60000000-0000-4000-8000-000000000001"`
	BINumber                  string `json:"bin_number"                  example:"BIN-123456789"`
	BusinessRegisteredAddress string `json:"business_registered_address" example:"1 High Street, London, UK"`
	BusinessTypeID            string `json:"business_type_id"            example:"61000000-0000-4000-8000-000000000001"`
	IndustryID                string `json:"industry_id"                 example:"62000000-0000-4000-8000-000000000001"`
}

// BusinessResponse is the business onboarding response payload.
type BusinessResponse struct {
	UserID                    string `json:"user_id"`
	LegalBusinessName         string `json:"legal_business_name"`
	LegalFullName             string `json:"legal_full_name"`
	CountryID                 string `json:"country_id"`
	CompanyRoleID             string `json:"company_role_id"`
	BINumber                  string `json:"bin_number"`
	BusinessRegisteredAddress string `json:"business_registered_address"`
	BusinessTypeID            string `json:"business_type_id"`
	IndustryID                string `json:"industry_id"`
}

// BusinessEnvelope is a successful business onboarding response.
type BusinessEnvelope struct {
	Data BusinessResponse `json:"data"`
	Meta *apidoc.Meta     `json:"meta,omitempty"`
}
