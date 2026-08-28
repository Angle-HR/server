package onboarding

import "testing"

func TestValidateIdentificationUK(t *testing.T) {
	t.Parallel()

	if err := ValidateIdentification("united-kingdom", map[string]string{
		"registration_number": "12345678",
	}); err != nil {
		t.Fatalf("valid CRN: %v", err)
	}

	if err := ValidateIdentification("united-kingdom", map[string]string{
		"registration_number": "SC123456",
	}); err != nil {
		t.Fatalf("valid SC CRN: %v", err)
	}

	if err := ValidateIdentification("united-kingdom", map[string]string{}); err == nil {
		t.Fatal("expected required field error")
	}
}

func TestPrimaryIdentificationNumber(t *testing.T) {
	t.Parallel()

	got := PrimaryIdentificationNumber("united-states", map[string]string{
		"state_of_incorporation": "DE",
		"file_number":            "1234567",
	})
	if got != "DE:1234567" {
		t.Fatalf("got %q", got)
	}
}
