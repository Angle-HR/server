package kyb

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// DefaultPRHURL is the Finnish Patent and Registration Office (PRH) open data YTJ v3 API base URL.
const DefaultPRHURL = "https://avoindata.prh.fi/opendata-ytj-api/v3"

const (
	fiBusinessIDDigits = 8 // digits in a Y-tunnus, the last being the check digit
	prhCurrentName     = "1"
	prhStreetAddress   = 1
)

var fiBusinessID = regexp.MustCompile(`^\d{7}-\d$`)

// PRH verifies Finnish companies against the PRH YTJ v3 API (Tier 1, free, no key).
type PRH struct {
	BaseURL string       // defaults to DefaultPRHURL
	Client  *http.Client // defaults to a client with a 10 second timeout
}

type prhResponse struct {
	Companies []prhCompany `json:"companies"`
}

type prhCompany struct {
	EndDate string `json:"endDate"`
	Names   []struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		EndDate string `json:"endDate"`
	} `json:"names"`
	Addresses []struct {
		Type       int    `json:"type"`
		Street     string `json:"street"`
		Building   string `json:"buildingNumber"`
		Entrance   string `json:"entrance"`
		Apartment  string `json:"apartmentNumber"`
		PostCode   string `json:"postCode"`
		EndDate    string `json:"endDate"`
		PostOffice []struct {
			City string `json:"city"`
		} `json:"postOffices"`
	} `json:"addresses"`
}

// NormalizeFinnishNumber strips spaces and inserts the hyphen before the check digit when it is missing.
func NormalizeFinnishNumber(n string) string {
	n = strings.ReplaceAll(strings.TrimSpace(n), " ", "")
	if len(n) == fiBusinessIDDigits && strings.Trim(n, "0123456789") == "" {
		n = n[:fiBusinessIDDigits-1] + "-" + n[fiBusinessIDDigits-1:]
	}
	return n
}

// Verify implements Verifier.
func (p *PRH) Verify(ctx context.Context, req *Request) (Result, error) {
	number := NormalizeFinnishNumber(req.RegistrationNumber)
	if !fiBusinessID.MatchString(number) {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	base := p.BaseURL
	if base == "" {
		base = DefaultPRHURL
	}
	var out prhResponse
	notFound, err := registryGet(ctx, p.Client, base, "/companies?businessId="+url.QueryEscape(number), &out)
	if err != nil {
		return Result{}, err
	}
	if notFound || len(out.Companies) == 0 {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	co := &out.Companies[0]
	return assessRecord(req, co.name(), co.address(), strings.TrimSpace(co.EndDate) != ""), nil
}

// name returns the current official name: the open-ended entry of type 1, else the first open-ended one.
func (c *prhCompany) name() string {
	fallback := ""
	for _, n := range c.Names {
		if strings.TrimSpace(n.EndDate) != "" {
			continue
		}
		if n.Type == prhCurrentName {
			return n.Name
		}
		if fallback == "" {
			fallback = n.Name
		}
	}
	return fallback
}

// address returns the current street address, else the first open-ended one.
func (c *prhCompany) address() *Address {
	var found *Address
	for _, a := range c.Addresses {
		if strings.TrimSpace(a.EndDate) != "" {
			continue
		}
		city := ""
		if len(a.PostOffice) > 0 {
			city = a.PostOffice[0].City
		}
		addr := &Address{
			Line1:    strings.TrimSpace(strings.Join(strings.Fields(a.Street+" "+a.Building+" "+a.Entrance+" "+a.Apartment), " ")),
			City:     city,
			PostCode: a.PostCode,
			Country:  "Finland",
		}
		if a.Type == prhStreetAddress {
			return addr
		}
		if found == nil {
			found = addr
		}
	}
	if found == nil {
		return &Address{}
	}
	return found
}
