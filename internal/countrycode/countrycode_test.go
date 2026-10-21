package countrycode

import "testing"

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"GB": "GB", " gb ": "GB", "UK": "GB", "uk": "GB", "ng": "NG", "KE": "KE"} {
		got, ok := Normalize(in)
		if !ok || got != want {
			t.Errorf("Normalize(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "U", "UKK", "XX", "ZZ", "AA", "1A", "EU", "UN"} {
		if got, ok := Normalize(in); ok {
			t.Errorf("Normalize(%q) = %q, true; want invalid", in, got)
		}
	}
}

func TestAssignedCount(t *testing.T) {
	if len(assigned) != 249 {
		t.Errorf("ISO 3166-1 assigns 249 codes, list has %d", len(assigned))
	}
}
