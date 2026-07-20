package handler

import "github.com/Angle-HR/server/internal/apidoc"

// Ensures swag resolves apidoc.ErrorEnvelope for handler package comments.
var _ = apidoc.ErrorEnvelope{}

// CountryResponse is a country option for the waitlist form.
type CountryResponse struct {
	ID      string  `json:"id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
	Name    string  `json:"name" example:"United Kingdom"`
	Slug    string  `json:"slug" example:"united-kingdom"`
	Region  string  `json:"region" example:"uk" enums:"uk,us,africa,eu,asia"`
	IconKey *string `json:"icon_key,omitempty" example:"flag-uk"`
}

// CountriesEnvelope is a successful countries list response.
type CountriesEnvelope struct {
	Data []CountryResponse `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// SignupRequest is the waitlist signup request body.
type SignupRequest struct {
	FullName  string `json:"full_name" example:"Jerry"`
	Email     string `json:"email" example:"jerry@example.com"`
	CountryID string `json:"country_id" example:"a1b2c3d4-e5f6-4789-a012-3456789abcde"`
}

// SignupData is the waitlist signup success payload.
type SignupData struct {
	Message string `json:"message" example:"You're on the list!"`
	Region  string `json:"region" example:"uk"`
	Token   string `json:"token" example:"550e8400-e29b-41d4-a716-446655440000"`
}

// SignupEnvelope is a successful waitlist signup response.
type SignupEnvelope struct {
	Data SignupData   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// IndustryListEnvelope is a catalog industries response.
type IndustryListEnvelope struct {
	Data []Industry   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// HiringToolListEnvelope is a catalog hiring tools response.
type HiringToolListEnvelope struct {
	Data []HiringTool `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// HiringFrustrationListEnvelope is a catalog hiring frustrations response.
type HiringFrustrationListEnvelope struct {
	Data []HiringFrustration `json:"data"`
	Meta *apidoc.Meta        `json:"meta,omitempty"`
}

// RoleListEnvelope is a catalog roles response.
type RoleListEnvelope struct {
	Data []Role       `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// TeamSizeListEnvelope is a catalog team sizes response.
type TeamSizeListEnvelope struct {
	Data []TeamSize   `json:"data"`
	Meta *apidoc.Meta `json:"meta,omitempty"`
}

// OnboardingRequest is the full onboarding submit payload.
type OnboardingRequest struct {
	Token            string   `json:"token" example:"550e8400-e29b-41d4-a716-446655440000"`
	IndustryIDs      []string `json:"industry_ids"`
	OtherIndustry    *string  `json:"other_industry,omitempty"`
	ToolIDs          []string `json:"tool_ids"`
	OtherTool        *string  `json:"other_tool,omitempty"`
	FrustrationIDs   []string `json:"frustration_ids"`
	OtherFrustration *string  `json:"other_frustration,omitempty"`
	RoleID           string   `json:"role_id"`
	TeamSizeID       string   `json:"team_size_id"`
	WantsEarlyAccess bool     `json:"wants_early_access"`
	WantsUserTesting bool     `json:"wants_user_testing"`
}

// OnboardingData is the onboarding submit success payload.
type OnboardingData struct {
	Message string `json:"message" example:"Thanks for telling us more!"`
}

// OnboardingEnvelope is a successful onboarding submit response.
type OnboardingEnvelope struct {
	Data OnboardingData `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}
