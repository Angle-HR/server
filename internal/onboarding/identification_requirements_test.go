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

func TestValidateIdentificationNigeria(t *testing.T) {
	t.Parallel()

	if err := ValidateIdentification("nigeria", map[string]string{
		"registration_number": "1234567",
	}); err != nil {
		t.Fatalf("valid 7-digit RC: %v", err)
	}

	if err := ValidateIdentification("nigeria", map[string]string{
		"registration_number": "RC1234567",
	}); err != nil {
		t.Fatalf("valid RC-prefixed number: %v", err)
	}

	if err := ValidateIdentification("nigeria", map[string]string{
		"registration_number": "RC 1234567",
	}); err != nil {
		t.Fatalf("valid RC-prefixed number with space: %v", err)
	}
}

func TestPrimaryIdentificationNumberNigeria(t *testing.T) {
	t.Parallel()

	got := PrimaryIdentificationNumber("nigeria", map[string]string{
		"registration_number": "RC1234567",
	})
	if got != "1234567" {
		t.Fatalf("got %q, want normalized 7 digits", got)
	}

	got = PrimaryIdentificationNumber("nigeria", map[string]string{
		"registration_number": "1234567",
	})
	if got != "1234567" {
		t.Fatalf("got %q", got)
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
