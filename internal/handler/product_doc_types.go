package handler

import "github.com/Angle-HR/server/internal/apidoc"

// Product onboarding OpenAPI types (design-only; handlers return 501 until implemented).

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
	Email                  string `json:"email" example:"jerry@example.com"`
	CodeExpiresInSeconds   int    `json:"code_expires_in_seconds" example:"60"`
	ResendAvailableInSeconds int  `json:"resend_available_in_seconds" example:"30"`
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
	Status          string   `json:"status" example:"in_progress" enums:"in_progress,completed"`
	CurrentStep     *string  `json:"current_step,omitempty" example:"profile"`
	CompletedSteps  []string `json:"completed_steps" example:"verify_email,profile"`
	NextStep        *string  `json:"next_step,omitempty" example:"address"`
}

// AuthTokenData is returned after verify-email or login.
type AuthTokenData struct {
	AccessToken  string                    `json:"access_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	RefreshToken string                    `json:"refresh_token" example:"eyJhbGciOiJIUzI1NiIs..."`
	ExpiresIn    int                       `json:"expires_in" example:"3600"`
	Onboarding   OnboardingProgressSummary `json:"onboarding"`
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

// ProductProfileRequest upserts account type and profile fields.
type ProductProfileRequest struct {
	AccountType         string  `json:"account_type" example:"business" enums:"individual,business"`
	FirstName           *string `json:"first_name,omitempty" example:"Jerry"`
	LastName            *string `json:"last_name,omitempty" example:"Oluwasegun"`
	CountryID           *string `json:"country_id,omitempty" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	LegalBusinessName   *string `json:"legal_business_name,omitempty" example:"ANGLE"`
	LegalFullName       *string `json:"legal_full_name,omitempty" example:"Jerry Oluwasegun"`
	CompanyRoleID       *string `json:"company_role_id,omitempty" example:"60000000-0000-4000-8000-000000000001"`
}

// ProductProfileData is the profile step response payload.
type ProductProfileData struct {
	AccountType         string                    `json:"account_type" example:"business"`
	FirstName           *string                   `json:"first_name,omitempty"`
	LastName            *string                   `json:"last_name,omitempty"`
	CountryID           *string                   `json:"country_id,omitempty"`
	LegalBusinessName   *string                   `json:"legal_business_name,omitempty"`
	LegalFullName       *string                   `json:"legal_full_name,omitempty"`
	CompanyRoleID       *string                   `json:"company_role_id,omitempty"`
	Region              string                    `json:"region" example:"uk"`
	Onboarding          OnboardingProgressSummary `json:"onboarding"`
}

// ProductProfileEnvelope is a successful profile upsert response.
type ProductProfileEnvelope struct {
	Data ProductProfileData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// ProductAddressRequest upserts workspace address.
type ProductAddressRequest struct {
	CountryID         string  `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	EntryMode         string  `json:"entry_mode" example:"manual" enums:"search,manual"`
	Line1             string  `json:"line_1" example:"10 Downing Street"`
	Line2             *string `json:"line_2,omitempty"`
	City              string  `json:"city" example:"London"`
	StateOrCounty     string  `json:"state_or_county" example:"Greater London"`
	PostCode          string  `json:"post_code" example:"SW1A 2AA"`
	FormattedAddress  *string `json:"formatted_address,omitempty"`
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
	VerificationStatus string                    `json:"verification_status" example:"unverified" enums:"unverified,verified,failed"`
	Onboarding         OnboardingProgressSummary `json:"onboarding"`
}

// ProductAddressEnvelope is a successful address upsert response.
type ProductAddressEnvelope struct {
	Data ProductAddressData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// ProductBusinessRequest upserts business compliance fields.
type ProductBusinessRequest struct {
	BusinessTypeID string `json:"business_type_id" example:"61000000-0000-4000-8000-000000000001"`
	IndustryID     string `json:"industry_id" example:"62000000-0000-4000-8000-000000000001"`
	EmployeeCount  int    `json:"employee_count" example:"25"`
}

// ProductBusinessData is the business step response payload.
type ProductBusinessData struct {
	BusinessTypeID string                    `json:"business_type_id"`
	IndustryID     string                    `json:"industry_id"`
	EmployeeCount  int                       `json:"employee_count"`
	Onboarding     OnboardingProgressSummary `json:"onboarding"`
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
	CountryID          string  `json:"country_id"`
	EntryMode          string  `json:"entry_mode"`
	Line1              string  `json:"line_1"`
	Line2              *string `json:"line_2,omitempty"`
	City               string  `json:"city"`
	StateOrCounty      string  `json:"state_or_county"`
	PostCode           string  `json:"post_code"`
	FormattedAddress   *string `json:"formatted_address,omitempty"`
	VerificationStatus string `json:"verification_status"`
}

// ProductBusinessState is saved business fields in status response.
type ProductBusinessState struct {
	BusinessTypeID string `json:"business_type_id"`
	IndustryID     string `json:"industry_id"`
	EmployeeCount  int    `json:"employee_count"`
}

// ProductOnboardingStatusData is the full onboarding state.
type ProductOnboardingStatusData struct {
	Status          string                `json:"status" example:"in_progress" enums:"in_progress,completed"`
	AccountType     *string               `json:"account_type,omitempty" example:"business"`
	CurrentStep     *string               `json:"current_step,omitempty" example:"address"`
	CompletedSteps  []string              `json:"completed_steps"`
	NextStep        *string               `json:"next_step,omitempty" example:"address"`
	Profile         *ProductProfileState  `json:"profile,omitempty"`
	Address         *ProductAddressState  `json:"address,omitempty"`
	Business        *ProductBusinessState `json:"business,omitempty"`
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
	RedirectURL string               `json:"redirect_url" example:"https://app.openhr.example/dashboard"`
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
