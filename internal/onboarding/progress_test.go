package onboarding

import "testing"

func TestNextStep(t *testing.T) {
	t.Parallel()

	completed := []string{StepVerifyEmail, StepProfile}
	if got := NextStep(AccountIndividual, completed); got != StepAddress {
		t.Fatalf("got %q want address", got)
	}

	if got := NextStep(AccountBusiness, completed); got != StepAddress {
		t.Fatalf("got %q want address", got)
	}

	businessDone := []string{StepVerifyEmail, StepProfile, StepAddress, StepBusiness}
	if got := NextStep(AccountBusiness, businessDone); got != StepComplete {
		t.Fatalf("got %q want complete", got)
	}
}

func TestCanComplete(t *testing.T) {
	t.Parallel()

	individual := []string{StepVerifyEmail, StepProfile, StepAddress}
	if !CanComplete(AccountIndividual, individual) {
		t.Fatal("expected individual onboarding to be completable")
	}

	business := []string{StepVerifyEmail, StepProfile, StepAddress}
	if CanComplete(AccountBusiness, business) {
		t.Fatal("expected business onboarding to be incomplete without business step")
	}
}
