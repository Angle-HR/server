package countrycode

import "testing"

func TestSupportedFromEnv(t *testing.T) {
	t.Setenv(EnvSupported, "")
	f := SupportedFromEnv()
	for _, c := range []string{"GB", "DE", "IE", "FR", "SE"} {
		if !f(c) {
			t.Errorf("%s should be supported", c)
		}
	}
	for _, c := range []string{"KE", "US", "IN", "NG", "CH", "NO"} {
		if f(c) {
			t.Errorf("%s should not be supported", c)
		}
	}
	t.Setenv(EnvSupported, "any")
	if !SupportedFromEnv()("KE") {
		t.Error("any must allow KE")
	}
}
