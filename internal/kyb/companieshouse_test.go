package kyb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chServer(t *testing.T, status int, body string) (*httptest.Server, *string) {
	t.Helper()
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if ok && pass == "" {
			gotUser = user
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("test server write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gotUser
}

const activeBody = `{"company_name":"ACME LIMITED","company_number":"12345678","company_status":"active",
 "registered_office_address":{"address_line_1":"1 High Street","locality":"London","postal_code":"EC1A 1BB","country":"England"}}`

func req() *Request {
	return &Request{CountryCode: "GB", RegistrationNumber: "12345678", LegalName: "Acme Ltd",
		Address: Address{Line1: "1 High Street", PostCode: "EC1A 1BB"}}
}

func TestCompaniesHouseOutcomes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		mod    func(*Request)
		want   Outcome
		reason FailureReason
	}{
		{"verified", 200, activeBody, nil, OutcomeVerified, ""},
		{"number not found", 404, `{}`, nil, OutcomeFailed, ReasonNumberNotFound},
		{
			"dissolved",
			200,
			strings.Replace(activeBody, `"active"`, `"dissolved"`, 1),
			nil,
			OutcomeFailed,
			ReasonInactiveEntity,
		},
		{
			"liquidation",
			200,
			strings.Replace(activeBody, `"active"`, `"liquidation"`, 1),
			nil,
			OutcomeFailed,
			ReasonInactiveEntity,
		},
		{
			"name mismatch",
			200,
			activeBody,
			func(r *Request) { r.LegalName = "Acme Trading" },
			OutcomeFailed,
			ReasonNameMismatch,
		},
		{
			"address mismatch",
			200,
			activeBody,
			func(r *Request) { r.Address.PostCode = "M1 1AA" },
			OutcomeFailed,
			ReasonAddressMismatch,
		},
		{"inactive wins over name mismatch", 200, strings.Replace(activeBody, `"active"`, `"dissolved"`, 1),
			func(r *Request) { r.LegalName = "Other" }, OutcomeFailed, ReasonInactiveEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, user := chServer(t, c.status, c.body)
			v := &CompaniesHouse{BaseURL: srv.URL, APIKey: "test-key"}
			r := req()
			if c.mod != nil {
				c.mod(r)
			}
			res, err := v.Verify(context.Background(), r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Outcome != c.want || res.Reason != c.reason {
				t.Fatalf("got %s/%s, want %s/%s", res.Outcome, res.Reason, c.want, c.reason)
			}
			if *user != "test-key" {
				t.Errorf("API key was not sent as the Basic Auth username")
			}
		})
	}
}

func TestCompaniesHouseReturnsRegistryValuesOnMismatch(t *testing.T) {
	srv, _ := chServer(t, 200, activeBody)
	v := &CompaniesHouse{BaseURL: srv.URL, APIKey: "k"}
	r := req()
	r.LegalName = "Acme Trading"
	res, err := v.Verify(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if res.RegistryName != "ACME LIMITED" || !strings.Contains(res.RegisteredAddress, "EC1A 1BB") {
		t.Fatalf("registry values not returned: %+v", res)
	}
}

func TestCompaniesHouseCouldNotRun(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{
			401,
			ErrRegistryAuth,
		},
		{403, ErrRegistryAuth},
		{429, ErrRateLimited},
		{500, ErrRegistryUnavailable},
		{503, ErrRegistryUnavailable},
	}
	for _, c := range cases {
		srv, _ := chServer(t, c.status, `{}`)
		_, err := (&CompaniesHouse{BaseURL: srv.URL, APIKey: "k"}).Verify(context.Background(), req())
		if !errors.Is(err, c.want) {
			t.Errorf("status %d: err = %v, want %v", c.status, err, c.want)
		}
	}
	if _, err := (&CompaniesHouse{BaseURL: "http://127.0.0.1:1", APIKey: "secret-key"}).Verify(
		context.Background(),
		req(),
	); !errors.Is(err, ErrRegistryUnavailable) ||
		strings.Contains(err.Error(), "secret-key") {
		t.Errorf("connection error must be ErrRegistryUnavailable without leaking the key, got %v", err)
	}
	if _, err := (&CompaniesHouse{}).Verify(context.Background(), req()); !errors.Is(err, ErrRegistryAuth) {
		t.Errorf("missing API key should be ErrRegistryAuth, got %v", err)
	}
}

func TestRegistryTiers(t *testing.T) {
	reg := NewRegistry()
	reg.Register("gb", &CompaniesHouse{APIKey: "k"})
	if reg.TierFor("GB") != Tier1 || reg.TierFor("NG") != Tier2 {
		t.Fatal("GB should be tier 1 and NG tier 2")
	}
	res, tier, err := reg.Verify(context.Background(), &Request{CountryCode: "NG", RegistrationNumber: "RC1234567"})
	if err != nil || tier != Tier2 || res.Outcome != OutcomeNeedsReview {
		t.Fatalf("tier 2 country should need review, got %+v tier %d err %v", res, tier, err)
	}
}
