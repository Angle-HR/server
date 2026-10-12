package kyb

import "testing"

func TestNamesMatch(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Acme Ltd", "ACME LIMITED", true},
		{"Acme & Sons Ltd.", "ACME AND SONS LIMITED", true},
		{"The Widget Company Limited", "Widget Company", true},
		{"Acme Ltd", "Acme Holdings Ltd", false},
		{"Open HR Ltd", "Open HR Technologies Ltd", false},
		{"", "", false},
		{"Ltd", "Ltd", true}, // a name that is only a suffix is kept as is
	}
	for _, c := range cases {
		if got := NamesMatch(c.a, c.b); got != c.want {
			t.Errorf("NamesMatch(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestAddressesMatch(t *testing.T) {
	reg := &Address{Line1: "1 High Street", City: "London", PostCode: "EC1A 1BB"}
	cases := []struct {
		name string
		sub  Address
		want bool
	}{
		{"same postcode different spacing", Address{PostCode: "ec1a1bb"}, true},
		{"different postcode", Address{Line1: "1 High Street", PostCode: "SW1A 1AA"}, false},
		{"no postcode, first line overlaps", Address{Line1: "1 High Street"}, true},
		{"no postcode, no overlap", Address{Line1: "99 Other Road"}, false},
		{"empty", Address{}, false},
	}
	for _, c := range cases {
		if got := AddressesMatch(&c.sub, reg); got != c.want {
			t.Errorf("%s: AddressesMatch = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeUKNumber(t *testing.T) {
	cases := map[string]string{
		"12345678":  "12345678",
		" 123456 ":  "00123456",
		"sc 123456": "SC123456",
		"NI123456":  "NI123456",
		"":          "",
	}
	for in, want := range cases {
		if got := NormalizeUKNumber(in); got != want {
			t.Errorf("NormalizeUKNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDisplayStateAndGating(t *testing.T) {
	cases := []struct {
		s      Status
		r      FailureReason
		d      Display
		canPub bool
	}{
		{StatusNotStarted, "", DisplayNotStarted, false},
		{StatusPending, "", DisplayPendingReview, false},
		{StatusVerified, "", DisplayVerified, true},
		{StatusFailed, ReasonNameMismatch, DisplayActionRequired, false},
		{StatusFailed, ReasonInactiveEntity, DisplayFailed, false},
	}
	for _, c := range cases {
		if got := DisplayState(c.s, c.r); got != c.d {
			t.Errorf("DisplayState(%s,%s) = %s, want %s", c.s, c.r, got, c.d)
		}
		if c.s.CanPublish() != c.canPub {
			t.Errorf("%s CanPublish = %v, want %v", c.s, c.s.CanPublish(), c.canPub)
		}
		if !c.s.CanDraft() {
			t.Errorf("%s must always allow drafting", c.s)
		}
	}
}
