package onboarding

import "testing"

func TestRegistrationNumberFormatOK(t *testing.T) {
	cases := []struct {
		country, number string
		want            bool
	}{
		{"GB", "01234567", true},
		{"gb", " 1234567 ", true}, // short numbers are padded to eight digits
		{"GB", "sc123456", true},
		{"GB", "PVT-ABC123", false}, // a Kenyan number under the UK
		{"KE", "PVT-ABC123", true},
		{"KE", "01234567", false},
		{"NG", "RC 1234567", true},
		{"DE", "HRB 12345", true},
		{"DE", "12345", false},
		{"CZ", "27082440", true},
		{"CZ", "123", true}, // padded to eight digits
		{"CZ", "HRB 12345", false},
		{"FI", "0112038-9", true},
		{"FI", "01120389", true}, // hyphen added
		{"FI", "12345678-9", false},
		{"US", "anything", true}, // multi-identifier countries are left to the verifier
		{"FR", "anything", true},
	}
	for _, c := range cases {
		if got := RegistrationNumberFormatOK(c.country, c.number); got != c.want {
			t.Errorf("%s %q: got %v, want %v", c.country, c.number, got, c.want)
		}
	}
}
