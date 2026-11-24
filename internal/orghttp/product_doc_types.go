package orghttp

import "github.com/Angle-HR/server/internal/apidoc"

// Product onboarding OpenAPI types.

// OnboardingProgressSummary is embedded in auth and onboarding responses.
type OnboardingProgressSummary struct {
	Status         string   `json:"status" example:"in_progress" enums:"in_progress,completed"`
	CurrentStep    *string  `json:"current_step,omitempty" example:"profile"`
	CompletedSteps []string `json:"completed_steps" example:"verify_email,profile"`
	NextStep       *string  `json:"next_step,omitempty" example:"compliance"`
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

// RegionReissue is returned when an onboarding step moved the account out of the global holding region. The
// server rotated the session: a new cookie is set in the same response and the previous session id is dead, so
// the client must replace its stored CSRF token with this one.
type RegionReissue struct {
	CSRFToken string `json:"csrf_token" example:"2Yb7vZk0n1l3o9Qm5r8s4tA6uC1dE3fGhIjKlMnOpQw"`
	Region    string `json:"region" example:"uk"`
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
	Session           *RegionReissue            `json:"session,omitempty"`
}

// ProductProfileEnvelope is a successful profile upsert response.
type ProductProfileEnvelope struct {
	Data ProductProfileData `json:"data"`
	Meta *apidoc.Meta       `json:"meta,omitempty"`
}

// ProductAddressRequest upserts workspace address.
type ProductAddressRequest struct {
	CountryID        string            `json:"country_id,omitempty" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
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
	Session            *RegionReissue            `json:"session,omitempty"`
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
	// BusinessTypeID is empty (omitted) for an individual that did not send one.
	BusinessTypeID string                    `json:"business_type_id,omitempty"`
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
	Status         string                  `json:"status" example:"in_progress" enums:"in_progress,completed"`
	AccountType    *string                 `json:"account_type,omitempty" example:"business"`
	CurrentStep    *string                 `json:"current_step,omitempty" example:"identification_address"`
	CompletedSteps []string                `json:"completed_steps"`
	NextStep       *string                 `json:"next_step,omitempty" example:"compliance"`
	Profile        *ProductProfileState    `json:"profile,omitempty"`
	Address        *ProductAddressState    `json:"address,omitempty"`
	Compliance     *ProductComplianceState `json:"compliance,omitempty"`
	Business       *ProductBusinessState   `json:"business,omitempty"` // deprecated: use compliance
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
	// Organization is the personal organization an individual account got (absent for business accounts).
	Organization *ProductOrganizationStub `json:"organization,omitempty"`
}

// ProductOrganizationStub is the personal organization created by completing individual onboarding.
type ProductOrganizationStub struct {
	ID                 string `json:"id"`
	Kind               string `json:"kind" example:"individual" enums:"individual,business"`
	VerificationStatus string `json:"verification_status" example:"pending" enums:"not_started,pending,verified,failed"`
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
	UserID         string         `json:"user_id"`
	FirstName      string         `json:"first_name"`
	LastName       string         `json:"last_name"`
	CountryID      string         `json:"country_id"`
	BusinessTypeID string         `json:"business_type_id"`
	IndustryID     string         `json:"industry_id"`
	NoOfEmployees  int            `json:"no_of_employees"`
	Session        *RegionReissue `json:"session,omitempty"`
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
	UserID                    string         `json:"user_id"`
	LegalBusinessName         string         `json:"legal_business_name"`
	LegalFullName             string         `json:"legal_full_name"`
	CountryID                 string         `json:"country_id"`
	CompanyRoleID             string         `json:"company_role_id"`
	BINumber                  string         `json:"bin_number"`
	BusinessRegisteredAddress string         `json:"business_registered_address"`
	BusinessTypeID            string         `json:"business_type_id"`
	IndustryID                string         `json:"industry_id"`
	Session                   *RegionReissue `json:"session,omitempty"`
}

// BusinessEnvelope is a successful business onboarding response.
type BusinessEnvelope struct {
	Data BusinessResponse `json:"data"`
	Meta *apidoc.Meta     `json:"meta,omitempty"`
}
