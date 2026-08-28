package onboarding

import "testing"

func TestNextStep(t *testing.T) {
	t.Parallel()

	completed := []string{StepVerifyEmail, StepProfile}
	if got := NextStep(AccountIndividual, completed); got != StepCompliance {
		t.Fatalf("individual after profile: got %q want compliance", got)
	}

	if got := NextStep(AccountBusiness, completed); got != StepIdentificationAddress {
		t.Fatalf("business after profile: got %q want identification_address", got)
	}

	businessAfterID := []string{StepVerifyEmail, StepProfile, StepIdentificationAddress}
	if got := NextStep(AccountBusiness, businessAfterID); got != StepCompliance {
		t.Fatalf("business after identification_address: got %q want compliance", got)
	}

	businessDone := []string{StepVerifyEmail, StepProfile, StepIdentificationAddress, StepCompliance}
	if got := NextStep(AccountBusiness, businessDone); got != StepComplete {
		t.Fatalf("business complete: got %q want complete", got)
	}

	individualDone := []string{StepVerifyEmail, StepProfile, StepCompliance}
	if got := NextStep(AccountIndividual, individualDone); got != StepComplete {
		t.Fatalf("individual complete: got %q want complete", got)
	}
}

func TestNextStepLegacySteps(t *testing.T) {
	t.Parallel()

	legacyBusiness := []string{StepVerifyEmail, StepProfile, StepAddress, StepBusiness}
	if got := NextStep(AccountBusiness, legacyBusiness); got != StepComplete {
		t.Fatalf("legacy business steps: got %q want complete", got)
	}

	legacyAfterProfile := []string{StepVerifyEmail, StepProfile}
	if got := NextStep(AccountBusiness, append(legacyAfterProfile, StepAddress)); got != StepCompliance {
		t.Fatalf("legacy address only: got %q want compliance", got)
	}
}

func TestCanComplete(t *testing.T) {
	t.Parallel()

	individual := []string{StepVerifyEmail, StepProfile, StepCompliance}
	if !CanComplete(AccountIndividual, individual) {
		t.Fatal("expected individual onboarding to be completable")
	}

	business := []string{StepVerifyEmail, StepProfile, StepIdentificationAddress}
	if CanComplete(AccountBusiness, business) {
		t.Fatal("expected business onboarding to be incomplete without compliance step")
	}
}

func TestNormalizeCompletedSteps(t *testing.T) {
	t.Parallel()

	got := NormalizeCompletedSteps([]string{StepVerifyEmail, StepProfile, StepAddress, StepBusiness})
	want := []string{StepVerifyEmail, StepProfile, StepIdentificationAddress, StepCompliance}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
}
