package kyb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	ukNumberLength   = 8       // digits in a Companies House company number
	maxResponseBytes = 1 << 20 // upper bound on a response body we will read
	defaultTimeout   = 10 * time.Second
)

// DefaultCompaniesHouseURL is the Companies House Public Data API base URL.
const DefaultCompaniesHouseURL = "https://api.company-information.service.gov.uk"

// CompaniesHouse verifies UK companies against the Companies House Public Data
// API (Tier 1, free). The API key is sent as the Basic Auth username with an
// empty password. The key is never logged or included in errors.
type CompaniesHouse struct {
	BaseURL string       // defaults to DefaultCompaniesHouseURL
	APIKey  string       // required
	Client  *http.Client // defaults to a client with a 10 second timeout
}

type chCompany struct {
	CompanyName   string `json:"company_name"`
	CompanyNumber string `json:"company_number"`
	CompanyStatus string `json:"company_status"`
	Office        struct {
		Line1    string `json:"address_line_1"`
		Line2    string `json:"address_line_2"`
		Locality string `json:"locality"`
		Region   string `json:"region"`
		PostCode string `json:"postal_code"`
		Country  string `json:"country"`
	} `json:"registered_office_address"`
}

// activeStatuses are the Companies House company_status values treated as in
// good standing. Everything else (dissolved, liquidation, receivership,
// administration, voluntary-arrangement, insolvency-proceedings,
// converted-closed, closed, removed) is treated as inactive. This list should be
// checked against the current API documentation before launch.
var activeStatuses = map[string]bool{"active": true, "open": true, "registered": true}

// NormalizeUKNumber upper-cases a company number, strips spaces, and left-pads
// all-digit numbers shorter than eight digits, as Companies House expects.
func NormalizeUKNumber(n string) string {
	n = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(n), " ", ""))
	if n != "" && len(n) < ukNumberLength && strings.Trim(n, "0123456789") == "" {
		n = strings.Repeat("0", ukNumberLength-len(n)) + n
	}
	return n
}

// Verify implements Verifier.
func (c *CompaniesHouse) Verify(ctx context.Context, req *Request) (Result, error) {
	if c.APIKey == "" {
		return Result{}, ErrRegistryAuth
	}
	number := NormalizeUKNumber(req.RegistrationNumber)
	if number == "" {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	co, notFound, err := c.fetch(ctx, number)
	if err != nil {
		return Result{}, err
	}
	if notFound {
		return Result{Outcome: OutcomeFailed, Reason: ReasonNumberNotFound}, nil
	}
	return assess(req, co), nil
}

// fetch calls the API. notFound is true for a 404. A non-nil error means the
// check could not run (see ErrRegistryUnavailable, ErrRateLimited, ErrRegistryAuth).
func (c *CompaniesHouse) fetch(ctx context.Context, number string) (co *chCompany, notFound bool, err error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultCompaniesHouseURL
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/company/"+url.PathEscape(number), http.NoBody)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrRegistryUnavailable, err)
	}
	httpReq.SetBasicAuth(c.APIKey, "")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		// Do not wrap err: its text can contain the request URL and, in some
		// transports, credentials.
		return nil, false, ErrRegistryUnavailable
	}
	defer closeBody(resp.Body)

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusNotFound:
		return nil, true, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, ErrRegistryAuth
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, false, ErrRateLimited
	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, false, ErrRegistryUnavailable
	default:
		return nil, false, fmt.Errorf("%w: unexpected status %d", ErrRegistryUnavailable, resp.StatusCode)
	}

	co = &chCompany{}
	if decodeErr := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(co); decodeErr != nil {
		return nil, false, fmt.Errorf("%w: invalid response", ErrRegistryUnavailable)
	}
	return co, false, nil
}

// assess compares the registry record with the submission. Order matters: an
// inactive entity is a hard block whatever else differs.
func assess(req *Request, co *chCompany) Result {
	registry := Address{Line1: co.Office.Line1, Line2: co.Office.Line2, City: co.Office.Locality,
		Region: co.Office.Region, PostCode: co.Office.PostCode, Country: co.Office.Country}
	res := Result{RegistryName: co.CompanyName, RegisteredAddress: FormatAddress(&registry)}

	switch {
	case !activeStatuses[strings.ToLower(co.CompanyStatus)]:
		res.Outcome, res.Reason = OutcomeFailed, ReasonInactiveEntity
	case !NamesMatch(req.LegalName, co.CompanyName):
		res.Outcome, res.Reason = OutcomeFailed, ReasonNameMismatch
	case !AddressesMatch(&req.Address, &registry):
		res.Outcome, res.Reason = OutcomeFailed, ReasonAddressMismatch
	default:
		res.Outcome = OutcomeVerified
	}
	return res
}

// closeBody closes a response body. A close error after the body was read
// changes nothing for the caller, so it is only logged at debug level.
func closeBody(c io.Closer) {
	if err := c.Close(); err != nil {
		slog.Debug("kyb: closing registry response body", "error", err)
	}
}
