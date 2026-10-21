package kyb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const aresActive = `{"ico":"27082440","obchodniJmeno":"Acme s.r.o.",
 "sidlo":{"nazevUlice":"Templova","cisloDomovni":747,"cisloOrientacni":"5","nazevObce":"Praha","psc":11000},
 "seznamRegistraci":{"stavZdrojeVr":"AKTIVNI"}}`

const prhActive = `{"totalResults":1,"companies":[{"businessId":{"value":"0112038-9"},"endDate":null,
 "names":[{"name":"Old Name Oy","type":"1","endDate":"2015-01-01"},{"name":"Acme Oy","type":"1","endDate":null}],
 "addresses":[{"type":2,"street":"PL 1","postCode":"00100","endDate":null,"postOffices":[{"city":"HELSINKI"}]},
  {"type":1,"street":"Mannerheimintie","buildingNumber":"1","postCode":"00100","endDate":null,"postOffices":[{"city":"HELSINKI"}]}]}]}`

func registryServer(t *testing.T, status int, body string, gotPath *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gotPath != nil {
			*gotPath = r.URL.RequestURI()
		}
		w.WriteHeader(status)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("test server write: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func czReq() *Request {
	return &Request{CountryCode: "CZ", RegistrationNumber: "27082440", LegalName: "Acme sro",
		Address: Address{Line1: "Templova 747/5", PostCode: "110 00"}}
}

func fiReq() *Request {
	return &Request{CountryCode: "FI", RegistrationNumber: "0112038-9", LegalName: "ACME OY",
		Address: Address{Line1: "Mannerheimintie 1", PostCode: "00100"}}
}

type registryCase struct {
	name   string
	status int
	body   string
	mod    func(*Request)
	want   Outcome
	reason FailureReason
}

func runRegistryCases(t *testing.T, mk func(url string) Verifier, base func() *Request, cases []registryCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := registryServer(t, c.status, c.body, nil)
			r := base()
			if c.mod != nil {
				c.mod(r)
			}
			res, err := mk(srv.URL).Verify(context.Background(), r)
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if res.Outcome != c.want || res.Reason != c.reason {
				t.Fatalf("got %s/%s, want %s/%s", res.Outcome, res.Reason, c.want, c.reason)
			}
		})
	}
}

func TestARESOutcomes(t *testing.T) {
	runRegistryCases(t, func(u string) Verifier { return &ARES{BaseURL: u} }, czReq, []registryCase{
		{"verified", 200, aresActive, nil, OutcomeVerified, ""},
		{"not found", 404, `{"kod":"NENALEZENO"}`, nil, OutcomeFailed, ReasonNumberNotFound},
		{"bad input", 400, `{"kod":"CHYBA_VSTUPU"}`, nil, OutcomeFailed, ReasonNumberNotFound},
		{"ceased by date", 200, `{"obchodniJmeno":"Acme s.r.o.","datumZaniku":"2020-01-01","sidlo":{}}`, nil,
			OutcomeFailed, ReasonInactiveEntity},
		{"ceased by register", 200, `{"obchodniJmeno":"Acme s.r.o.","seznamRegistraci":{"stavZdrojeVr":"ZANIKLY"}}`, nil,
			OutcomeFailed, ReasonInactiveEntity},
		{"name mismatch", 200, aresActive, func(r *Request) { r.LegalName = "Other" }, OutcomeFailed, ReasonNameMismatch},
		{"address mismatch", 200, aresActive, func(r *Request) { r.Address.PostCode = "60200" },
			OutcomeFailed, ReasonAddressMismatch},
		{"malformed number never calls out", 500, ``, func(r *Request) { r.RegistrationNumber = "ABC" },
			OutcomeFailed, ReasonNumberNotFound},
	})
}

func TestARESRequestPathPadsNumber(t *testing.T) {
	var path string
	srv := registryServer(t, 200, aresActive, &path)
	r := czReq()
	r.RegistrationNumber = "205"
	if _, err := (&ARES{BaseURL: srv.URL}).Verify(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if path != "/ekonomicke-subjekty/00000205" {
		t.Fatalf("path %q", path)
	}
}

func TestPRHOutcomes(t *testing.T) {
	runRegistryCases(t, func(u string) Verifier { return &PRH{BaseURL: u} }, fiReq, []registryCase{
		{"verified", 200, prhActive, nil, OutcomeVerified, ""},
		{"empty result", 200, `{"totalResults":0,"companies":[]}`, nil, OutcomeFailed, ReasonNumberNotFound},
		{"not found", 404, `{}`, nil, OutcomeFailed, ReasonNumberNotFound},
		{"ended", 200, `{"companies":[{"endDate":"2020-05-05","names":[{"name":"Acme Oy","type":"1"}]}]}`, nil,
			OutcomeFailed, ReasonInactiveEntity},
		{"old name does not match", 200, prhActive, func(r *Request) { r.LegalName = "Old Name" },
			OutcomeFailed, ReasonNameMismatch},
		{"address mismatch", 200, prhActive, func(r *Request) { r.Address.PostCode = "33100" },
			OutcomeFailed, ReasonAddressMismatch},
		{"bad format never calls out", 500, ``, func(r *Request) { r.RegistrationNumber = "12" },
			OutcomeFailed, ReasonNumberNotFound},
	})
}

func TestPRHAddsHyphenToQuery(t *testing.T) {
	var path string
	srv := registryServer(t, 200, prhActive, &path)
	r := fiReq()
	r.RegistrationNumber = "01120389"
	if _, err := (&PRH{BaseURL: srv.URL}).Verify(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if path != "/companies?businessId=0112038-9" {
		t.Fatalf("path %q", path)
	}
}

func TestKeylessRegistriesReportInfrastructureErrors(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{{429, ErrRateLimited}, {503, ErrRegistryUnavailable}, {403, ErrRegistryAuth}, {418, ErrRegistryUnavailable}}
	for _, c := range cases {
		srv := registryServer(t, c.status, `{}`, nil)
		for name, v := range map[string]Verifier{"ares": &ARES{BaseURL: srv.URL}, "prh": &PRH{BaseURL: srv.URL}} {
			req := czReq()
			if name == "prh" {
				req = fiReq()
			}
			if _, err := v.Verify(context.Background(), req); !errors.Is(err, c.want) {
				t.Errorf("%s status %d: got %v, want %v", name, c.status, err, c.want)
			}
		}
	}
}

func TestNormalizeNameDottedAbbreviations(t *testing.T) {
	for _, pair := range [][2]string{{"Acme s.r.o.", "ACME sro"}, {"Acme, a.s.", "Acme AS"}, {"Acme Oyj", "Acme Oy"}} {
		if !NamesMatch(pair[0], pair[1]) {
			t.Errorf("%q should match %q", pair[0], pair[1])
		}
	}
}
