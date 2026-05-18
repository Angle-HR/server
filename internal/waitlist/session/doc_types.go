package session

import (
	"time"

	"github.com/Angle-HR/server/internal/apidoc"
)

// Step1Request is the step 1 save payload.
type Step1Request struct {
	IndustryIDs   []string `json:"industry_ids"`
	OtherIndustry *string  `json:"other_industry"`
}

// Step2Request is the step 2 save payload.
type Step2Request struct {
	ToolIDs   []string `json:"tool_ids"`
	OtherTool *string  `json:"other_tool"`
}

// Step3Request is the step 3 save payload.
type Step3Request struct {
	FrustrationIDs   []string `json:"frustration_ids"`
	OtherFrustration *string  `json:"other_frustration"`
}

// Step4Request is the step 4 save payload.
type Step4Request struct {
	RoleID     string `json:"role_id"`
	TeamSizeID string `json:"team_size_id"`
}

// SubmitRequest is the final onboarding submit payload.
type SubmitRequest struct {
	Name             string `json:"name"`
	WantsEarlyAccess bool   `json:"wants_early_access"`
	WantsUserTesting bool   `json:"wants_user_testing"`
}

// CreateSessionEnvelope is the create session response.
type CreateSessionEnvelope struct {
	Data CreateSessionData `json:"data"`
	Meta *apidoc.Meta      `json:"meta,omitempty"`
}

// CreateSessionData is the session creation payload.
type CreateSessionData struct {
	SessionToken string    `json:"session_token" example:"550e8400-e29b-41d4-a716-446655440000"`
	CurrentStep  int       `json:"current_step" example:"1"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// GetSessionEnvelope is the get session response.
type GetSessionEnvelope struct {
	Data GetSessionData `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}

// GetSessionData is saved session progress.
type GetSessionData struct {
	CurrentStep int             `json:"current_step" example:"2"`
	Partial     PartialResponse `json:"partial"`
}

// StepProgressEnvelope is returned after saving a step.
type StepProgressEnvelope struct {
	Data StepProgressData `json:"data"`
	Meta *apidoc.Meta     `json:"meta,omitempty"`
}

// StepProgressData holds the next step number.
type StepProgressData struct {
	CurrentStep int `json:"current_step" example:"3"`
}

// SubmitEnvelope is the submit session response.
type SubmitEnvelope struct {
	Data SubmitResponse `json:"data"`
	Meta *apidoc.Meta   `json:"meta,omitempty"`
}
