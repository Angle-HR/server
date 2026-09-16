package onboarding

import "slices"

const (
	StepVerifyEmail           = "verify_email"
	StepProfile               = "profile"
	StepAddress               = "address" // legacy progress marker; maps to StepIdentificationAddress
	StepIdentificationAddress = "identification_address"
	StepCompliance            = "compliance"
	StepOnboarding            = "onboarding"
	StepBusiness              = "business" // legacy progress marker; maps to StepCompliance
	StepComplete              = "complete"
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
	normalized := NormalizeCompletedSteps(completed)
	for _, step := range required {
		if !slices.Contains(normalized, step) {
			return step
		}
	}

	return StepComplete
}

// RequiredSteps returns mandatory steps before completion.
func RequiredSteps(accountType string) []string {
	switch accountType {
	case AccountBusiness:
		return []string{StepVerifyEmail, StepProfile, StepIdentificationAddress, StepCompliance}
	default:
		return []string{StepVerifyEmail, StepProfile, StepCompliance}
	}
}

// CanComplete reports whether all mandatory steps are done.
func CanComplete(accountType string, completed []string) bool {
	return NextStep(accountType, completed) == StepComplete
}

// NormalizeCompletedSteps maps legacy step names to current ones.
func NormalizeCompletedSteps(completed []string) []string {
	if len(completed) == 0 {
		return completed
	}

	out := make([]string, 0, len(completed))
	seen := make(map[string]struct{}, len(completed))
	for _, step := range completed {
		normalized := normalizeStep(step)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	return out
}

func normalizeStep(step string) string {
	switch step {
	case StepAddress:
		return StepIdentificationAddress
	case StepBusiness:
		return StepCompliance
	default:
		return step
	}
}

// AdvanceCompleted appends step if missing and returns updated slice.
func AdvanceCompleted(completed []string, step string) []string {
	step = normalizeStep(step)
	normalized := NormalizeCompletedSteps(completed)
	if slices.Contains(normalized, step) {
		return normalized
	}

	return append(normalized, step)
}

// InitialProgress returns progress after email verification.
func InitialProgress() (currentStep string, completed []string) {
	return StepProfile, []string{StepVerifyEmail}
}
