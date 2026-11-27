package orgstore

import "testing"

func TestValidateSlug(t *testing.T) {
	ok := []string{"acme", "acme-hr", "a1b", "my-company-2026", "abc"}
	bad := []string{"", "ab", "-acme", "acme-", "ac--me", "Acme", "acme corp", "acme_corp", "acme.io",
		"admin", "api", "login", "company", "openhr", "j", "www",
		"this-address-is-way-too-long-for-a-careers-page"}
	for _, s := range ok {
		if why := ValidateSlug(s); why != "" {
			t.Errorf("%q should be valid, got %q", s, why)
		}
	}
	for _, s := range bad {
		if ValidateSlug(s) == "" {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestNormalizeSlug(t *testing.T) {
	if got := NormalizeSlug("  Acme-HR "); got != "acme-hr" {
		t.Errorf("got %q", got)
	}
}

func TestSuggestSlug(t *testing.T) {
	cases := map[string]string{
		"Acme Ltd":             "acme-ltd",
		"  Acme & Sons, Inc. ": "acme-sons-inc",
		"Café Société":         "caf-soci-t",
		"!!!":                  "",
		"Admin":                "", // reserved
		"AB":                   "", // too short
		"The Quick Brown Fox Jumps Over The Lazy Dog Ltd": "the-quick-brown-fox-jumps-over-the-lazy",
	}
	for in, want := range cases {
		if got := SuggestSlug(in); got != want {
			t.Errorf("SuggestSlug(%q) = %q, want %q", in, got, want)
		}
		if got := SuggestSlug(in); got != "" && ValidateSlug(got) != "" {
			t.Errorf("suggestion %q for %q is not valid", got, in)
		}
	}
}

func TestProfileName(t *testing.T) {
	if (Profile{LegalName: "Acme Ltd"}).Name() != "Acme Ltd" {
		t.Error("falls back to the legal name")
	}
	if (Profile{LegalName: "Acme Ltd", DisplayName: "Acme"}).Name() != "Acme" {
		t.Error("display name wins")
	}
}
