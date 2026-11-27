package applications

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOfferToken(t *testing.T) {
	tenant := "11111111-2222-3333-4444-555555555555"
	tok, hash, err := NewOfferToken("uk", tenant)
	if err != nil {
		t.Fatal(err)
	}
	reg, ten, h, ok := ParseOfferToken(tok)
	if !ok || reg != "uk" || ten != tenant || h != hash {
		t.Fatalf("parse = %q %q %q %v", reg, ten, h, ok)
	}
	if strings.Contains(hash, tok[strings.LastIndex(tok, ".")+1:]) {
		t.Fatal("the hash contains the secret")
	}
	tok2, _, _ := NewOfferToken("uk", tenant)
	if tok == tok2 {
		t.Fatal("tokens repeat")
	}
	for _, bad := range []string{"", "uk", "uk.x.y", "uk." + tenant + ".short", "UK." + tenant + "." + strings.Repeat("a", 43), "uk.not-a-uuid-not-a-uuid-not-a-uuid-1234." + strings.Repeat("a", 43), tok + "x", "../" + tok} {
		if _, _, _, ok := ParseOfferToken(bad); ok {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestOfferDocHash(t *testing.T) {
	a := OfferDocHash("T", "B", "£1", "2027-01-01")
	if a != OfferDocHash("T", "B", "£1", "2027-01-01") {
		t.Fatal("hash is not stable")
	}
	// moving text between fields must change the hash
	if a == OfferDocHash("TB", "", "£1", "2027-01-01") || a == OfferDocHash("T", "B", "£2", "2027-01-01") || a == OfferDocHash("T", "B", "£1", "") {
		t.Fatal("hash ignores a change")
	}
	if len(ConsentHash()) != 64 {
		t.Fatal("consent hash")
	}
}

func TestOfferPlaceholders(t *testing.T) {
	if err := checkOfferPlaceholders("body", "Dear {{candidate_name}}, start {{start_date}} for {{compensation}}"); err != nil {
		t.Fatal(err)
	}
	if err := checkOfferPlaceholders("body", "Hi {{password}}"); err == nil {
		t.Fatal("unknown placeholder accepted")
	}
}

// unusedOffers fails the test if the store is reached: sign input is checked first.
type unusedOffers struct{ OfferStore }

func (unusedOffers) OfferByToken(context.Context, string, string) (*OfferRef, error) {
	return nil, errors.New("store reached")
}

func TestSignOfferChecksInput(t *testing.T) {
	s := &Service{Contracts: unusedOffers{}}
	for name, c := range map[string]struct {
		full    string
		consent bool
	}{"no consent": {"Ada Lovelace", false}, "empty name": {"  ", true}, "one letter": {"A", true},
		"control char": {"Ada\x00 L", true}, "too long": {strings.Repeat("a", 101), true}} {
		_, err := s.SignOffer(context.Background(), "t", "h", c.full, c.consent, Meta{})
		if err == nil || err.Error() == "store reached" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := (&Service{}).SignOffer(context.Background(), "t", "h", "Ada Lovelace", true, Meta{}); !errors.Is(err, ErrNoOffers) {
		t.Errorf("no store = %v", err)
	}
}
