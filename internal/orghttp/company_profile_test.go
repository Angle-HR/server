package orghttp

import (
	"strings"
	"testing"
)

func fieldPaths(errs []fieldError) map[string]bool {
	m := map[string]bool{}
	for _, e := range errs {
		m[e.Path] = true
	}
	return m
}

func TestValidateProfileAcceptsAndNormalizes(t *testing.T) {
	in := ProfileInput{Slug: "  Acme-HR ", DisplayName: " Acme ", About: " Hello ", Website: "https://acme.example", LogoURL: "https://cdn.example/l.png"}
	if errs := validateProfile(&in); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if in.Slug != "acme-hr" || in.DisplayName != "Acme" || in.About != "Hello" {
		t.Errorf("not normalized: %+v", in)
	}
}

func TestValidateProfileAllowsEmpty(t *testing.T) {
	in := ProfileInput{}
	if errs := validateProfile(&in); len(errs) != 0 {
		t.Fatalf("an empty profile is valid: %v", errs)
	}
}

func TestValidateProfileRejects(t *testing.T) {
	in := ProfileInput{
		Slug:        "admin",
		DisplayName: strings.Repeat("a", profileNameMax+1),
		About:       strings.Repeat("a", profileAboutMax+1),
		Website:     "http://acme.example",
		LogoURL:     "javascript:alert(1)",
	}
	got := fieldPaths(validateProfile(&in))
	for _, p := range []string{"slug", "display_name", "about", "website", "logo_url"} {
		if !got[p] {
			t.Errorf("expected an error on %s, got %v", p, got)
		}
	}
}

func TestHTTPSURL(t *testing.T) {
	for _, s := range []string{"https://a.example", "https://a.example/x?y=1"} {
		if !httpsURL(s) {
			t.Errorf("%q should pass", s)
		}
	}
	for _, s := range []string{"http://a.example", "https://", "https://user:pw@a.example", "//a.example", "a.example", "ftp://a.example"} {
		if httpsURL(s) {
			t.Errorf("%q should fail", s)
		}
	}
}
