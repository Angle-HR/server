package kyb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// DefaultARESURL is the ARES (Czech Ministry of Finance) REST API base URL.
const DefaultARESURL = "https://ares.gov.cz/ekonomicke-subjekty-v-be/rest"

const (
	czNumberLength = 8 // digits in an IČO
	aresCeased     = "ZANIKLY"
)

// ARES verifies Czech companies against the ARES REST API (Tier 1, free, no key).
type ARES struct {
	BaseURL string       // defaults to DefaultARESURL
	Client  *http.Client // defaults to a client with a 10 second timeout
}

type aresSubject struct {
	Name      string   `json:"obchodniJmeno"`
	CeasedOn  string   `json:"datumZaniku"`
	Seat      aresSeat `json:"sidlo"`
	Registers struct {
		Commercial string `json:"stavZdrojeVr"` // commercial register state
	} `json:"seznamRegistraci"`
}

type aresSeat struct {
	Street   string      `json:"nazevUlice"`
	House    json.Number `json:"cisloDomovni"`
	Orient   json.Number `json:"cisloOrientacni"`
	District string      `json:"nazevCastiObce"`
	City     string      `json:"nazevObce"`
	PostCode json.Number `json:"psc"`
	Text     string      `json:"textovaAdresa"`
}

// NormalizeCzechNumber strips spaces and left-pads an all-digit IČO to eight digits.
func NormalizeCzechNumber(n string) string {
	n = strings.ReplaceAll(strings.TrimSpace(n), " ", "")
	if n != "" && len(n) < czNumberLength && strings.Trim(n, "0123456789") == "" {
		n = strings.Repeat("0", czNumberLength-len(n)) + n
	}
	return n
}

// Verify implements Verifier.
func (a *ARES) Verify(ctx context.Context, req *Request) (Result, error) {
	number := NormalizeCzechNumber(req.RegistrationNumber)
	if len(number) != czNumberLength || strings.Trim(number, "0123456789") != "" {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	base := a.BaseURL
	if base == "" {
		base = DefaultARESURL
	}
	var s aresSubject
	notFound, err := registryGet(ctx, a.Client, base, "/ekonomicke-subjekty/"+url.PathEscape(number), &s)
	if err != nil {
		return Result{}, err
	}
	if notFound {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	return assessRecord(req, s.Name, s.address(), s.ceased()), nil
}

func (s *aresSubject) ceased() bool {
	return strings.TrimSpace(s.CeasedOn) != "" || strings.EqualFold(s.Registers.Commercial, aresCeased)
}

func (s *aresSubject) address() *Address {
	street := strings.TrimSpace(s.Seat.Street)
	if street == "" {
		street = strings.TrimSpace(s.Seat.District)
	}
	number := s.Seat.House.String()
	if o := s.Seat.Orient.String(); o != "" {
		number += "/" + o
	}
	line1 := strings.TrimSpace(street + " " + number)
	if line1 == "" {
		line1 = s.Seat.Text
	}
	return &Address{Line1: line1, City: s.Seat.City, PostCode: s.Seat.PostCode.String(), Country: "Czechia"}
}
