package onboarding

import "slices"

const (
	StepVerifyEmail = "verify_email"
	StepProfile     = "profile"
	StepAddress     = "address"
	StepBusiness    = "business"
	StepComplete    = "complete"
)

const (
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
)

const (
	AccountIndividual = "individual"
	AccountBusiness   = "business"
)

// NextStep returns the next required step for the account type.
func NextStep(accountType string, completed []string) string {
	required := RequiredSteps(accountType)
	for _, step := range required {
		if !slices.Contains(completed, step) {
			return step
		}
	}

	return StepComplete
}

// RequiredSteps returns mandatory steps before completion.
func RequiredSteps(accountType string) []string {
	switch accountType {
	case AccountBusiness:
		return []string{StepVerifyEmail, StepProfile, StepAddress, StepBusiness}
	default:
		return []string{StepVerifyEmail, StepProfile, StepAddress}
	}
}

// CanComplete reports whether all mandatory steps are done.
func CanComplete(accountType string, completed []string) bool {
	return NextStep(accountType, completed) == StepComplete
}

// AdvanceCompleted appends step if missing and returns updated slice.
func AdvanceCompleted(completed []string, step string) []string {
	if slices.Contains(completed, step) {
		return completed
	}

	return append(slices.Clone(completed), step)
}

// InitialProgress returns progress after email verification.
func InitialProgress() (currentStep string, completed []string) {
	return StepProfile, []string{StepVerifyEmail}
}
