package region

import "testing"

func TestRegionFromCountry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		iso    string
		region Region
		ok     bool
	}{
		{"GB", RegionUK, true},
		{"gb", RegionUK, true},
		{"US", RegionUS, true},
		{"CA", RegionUS, true},
		{"NG", RegionAfrica, true},
		{"DE", RegionEU, true},
		{"XX", RegionUnknown, false},
		{"", RegionUnknown, false},
	}

	for _, tt := range tests {
		t.Run(tt.iso, func(t *testing.T) {
			t.Parallel()
			region, ok := regionFromCountry(tt.iso)
			if ok != tt.ok {
				t.Fatalf("ok: got %v, want %v", ok, tt.ok)
			}
			if region != tt.region {
				t.Fatalf("region: got %q, want %q", region, tt.region)
			}
		})
	}
}
