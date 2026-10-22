package onboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestGoogle(t *testing.T, h http.HandlerFunc) *GoogleAddressProvider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	g := NewGoogleAddressProvider("test-key")
	g.placesURL, g.validationURL = srv.URL, srv.URL
	return g
}

func validationHandler(verdict string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"verdict":` + verdict + `}}`))
	}
}

func TestGoogleVerify(t *testing.T) {
	place := "place-1"
	base := ProductAddress{CountrySlug: "united-kingdom", EntryMode: "manual", Line1: "1 High St", City: "London", PostCode: "N1 1AA"}
	tests := []struct {
		name    string
		verdict string
		addr    ProductAddress
		want    string
		reason  string
	}{
		{"verified", `{"addressComplete":true,"validationGranularity":"PREMISE"}`, base, VerificationStatusVerified, ""},
		{"route only", `{"addressComplete":true,"validationGranularity":"ROUTE"}`, base, VerificationStatusFailed, FailureReasonInvalidAddress},
		{"incomplete", `{"validationGranularity":"PREMISE"}`, base, VerificationStatusFailed, FailureReasonInvalidAddress},
		{"unconfirmed", `{"addressComplete":true,"hasUnconfirmedComponents":true,"validationGranularity":"PREMISE"}`, base, VerificationStatusFailed, FailureReasonInvalidAddress},
		{"unsupported country", `{}`, ProductAddress{CountrySlug: "nigeria", Line1: "x"}, VerificationStatusFailed, FailureReasonNotVerifiable},
		{"search without place id", `{}`, ProductAddress{CountrySlug: "germany", EntryMode: "search"}, VerificationStatusFailed, FailureReasonNotVerifiable},
		{"search with place id", `{"addressComplete":true,"validationGranularity":"SUB_PREMISE"}`, ProductAddress{CountrySlug: "germany", EntryMode: "search", PlaceID: &place, Line1: "a", City: "Berlin", PostCode: "10115"}, VerificationStatusVerified, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newTestGoogle(t, validationHandler(tt.verdict))
			got, err := g.Verify(context.Background(), tt.addr)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tt.want {
				t.Fatalf("status = %q, want %q", got.Status, tt.want)
			}
			if tt.reason != "" && (got.FailureReason == nil || *got.FailureReason != tt.reason) {
				t.Fatalf("reason = %v, want %q", got.FailureReason, tt.reason)
			}
		})
	}
}

func TestGoogleVerifyRequest(t *testing.T) {
	var gotKey string
	var body struct {
		Address struct {
			RegionCode   string   `json:"regionCode"`
			AddressLines []string `json:"addressLines"`
			PostalCode   string   `json:"postalCode"`
		} `json:"address"`
	}
	g := newTestGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Goog-Api-Key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"result":{"verdict":{}}}`))
	})
	line2 := "Flat 2"
	_, err := g.Verify(context.Background(), ProductAddress{CountrySlug: "united-kingdom", EntryMode: "manual", Line1: "1 High St", Line2: &line2, PostCode: "N1 1AA"})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "test-key" || body.Address.RegionCode != "GB" || body.Address.PostalCode != "N1 1AA" || len(body.Address.AddressLines) != 2 {
		t.Fatalf("unexpected request: key=%q body=%+v", gotKey, body)
	}
}

func TestGoogleVerifyUpstreamErrorIsNotVerified(t *testing.T) {
	g := newTestGoogle(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	res, err := g.Verify(context.Background(), ProductAddress{CountrySlug: "united-kingdom", EntryMode: "manual", Line1: "1 High St"})
	if err == nil {
		t.Fatalf("expected error, got %+v", res)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaks key: %v", err)
	}
}

func TestGoogleSearch(t *testing.T) {
	var region []string
	g := newTestGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/places:autocomplete":
			var b struct {
				Regions []string `json:"includedRegionCodes"`
			}
			_ = json.NewDecoder(r.Body).Decode(&b)
			region = b.Regions
			_, _ = w.Write([]byte(`{"suggestions":[{"placePrediction":{"placeId":"p1","text":{"text":"1 High St, London"}}},{"queryPrediction":{}}]}`))
		case "/v1/places/p1":
			_, _ = w.Write([]byte(`{"formattedAddress":"1 High St, London N1 1AA, UK","addressComponents":[
				{"longText":"1","types":["street_number"]},{"longText":"High St","types":["route"]},
				{"longText":"London","types":["postal_town"]},{"longText":"Greater London","types":["administrative_area_level_2"]},
				{"longText":"N1 1AA","types":["postal_code"]}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	got, err := g.Search(context.Background(), "united-kingdom", "1 high")
	if err != nil {
		t.Fatal(err)
	}
	if len(region) != 1 || region[0] != "GB" {
		t.Fatalf("regions = %v", region)
	}
	want := AddressSuggestion{PlaceID: "p1", Description: "1 High St, London", Line1: "1 High St", City: "London", StateOrCounty: "Greater London", PostCode: "N1 1AA", FormattedAddress: "1 High St, London N1 1AA, UK"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v", got)
	}
}

func TestGoogleSearchUnsupportedCountry(t *testing.T) {
	g := newTestGoogle(t, func(http.ResponseWriter, *http.Request) { t.Error("should not call Google") })
	got, err := g.Search(context.Background(), "nigeria", "lagos")
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v", got, err)
	}
}
