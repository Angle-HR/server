package onboarding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Angle-HR/server/pkg/besteffort"
)

const (
	googlePlacesBaseURL     = "https://places.googleapis.com"
	googleValidationBaseURL = "https://addressvalidation.googleapis.com"
	googleHTTPTimeout       = 5 * time.Second
	googleMaxSuggestions    = 5
	googleMaxBody           = 1 << 20
	autocompleteMask        = "suggestions.placePrediction.placeId,suggestions.placePrediction.text.text"
)

// googleRegionCodes maps a country slug to its ISO 3166-1 alpha-2 code. The
// countries table has no ISO column, so supported countries are listed here.
// "european-union" is deliberately absent: it names no single country, so
// searches are not restricted and verification lets Google infer the country.
var googleRegionCodes = map[string]string{
	"united-kingdom": "GB",
	"austria":        "AT", "belgium": "BE", "bulgaria": "BG", "croatia": "HR",
	"cyprus": "CY", "czechia": "CZ", "denmark": "DK", "estonia": "EE",
	"finland": "FI", "france": "FR", "germany": "DE", "greece": "GR",
	"hungary": "HU", "ireland": "IE", "italy": "IT", "latvia": "LV",
	"lithuania": "LT", "luxembourg": "LU", "malta": "MT", "netherlands": "NL",
	"poland": "PL", "portugal": "PT", "romania": "RO", "slovakia": "SK",
	"slovenia": "SI", "spain": "ES", "sweden": "SE",
}

// GoogleAddressProvider searches and verifies addresses with Google Maps
// Platform: Places API (New) for autocomplete and the Address Validation API.
// It covers the UK and EU countries; other countries are not verifiable.
type GoogleAddressProvider struct {
	apiKey        string
	client        *http.Client
	placesURL     string
	validationURL string
}

// NewGoogleAddressProvider returns a provider that authenticates with apiKey.
func NewGoogleAddressProvider(apiKey string) *GoogleAddressProvider {
	return &GoogleAddressProvider{
		apiKey:        apiKey,
		client:        &http.Client{Timeout: googleHTTPTimeout},
		placesURL:     googlePlacesBaseURL,
		validationURL: googleValidationBaseURL,
	}
}

// googleRegion returns the ISO code for a country slug and whether Google serves
// it. An empty code with ok set is the pseudo-country "european-union".
func googleRegion(countrySlug string) (code string, ok bool) {
	if countrySlug == "european-union" {
		return "", true
	}
	code, ok = googleRegionCodes[countrySlug]
	return code, ok
}

// Search returns up to five address suggestions for a partial query.
func (g *GoogleAddressProvider) Search(ctx context.Context, countrySlug, query string) ([]AddressSuggestion, error) {
	region, ok := googleRegion(countrySlug)
	if !ok {
		return []AddressSuggestion{}, nil
	}

	body := map[string]any{"input": query, "languageCode": "en"}
	if region != "" {
		body["includedRegionCodes"] = []string{region}
	} else {
		body["includedRegionCodes"] = euRegionCodes()
	}
	var resp struct {
		Suggestions []struct {
			PlacePrediction *struct {
				PlaceID string `json:"placeId"`
				Text    struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"placePrediction"`
		} `json:"suggestions"`
	}
	if err := g.post(ctx, g.placesURL+"/v1/places:autocomplete", autocompleteMask, body, &resp); err != nil {
		return nil, fmt.Errorf("google autocomplete: %w", err)
	}

	type pred struct{ placeID, text string }
	var preds []pred
	for _, s := range resp.Suggestions {
		if s.PlacePrediction == nil || s.PlacePrediction.PlaceID == "" {
			continue
		}
		preds = append(preds, pred{s.PlacePrediction.PlaceID, s.PlacePrediction.Text.Text})
		if len(preds) == googleMaxSuggestions {
			break
		}
	}

	out := make([]AddressSuggestion, len(preds))
	errs := make([]error, len(preds))
	var wg sync.WaitGroup
	for i, p := range preds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i], errs[i] = g.placeDetails(ctx, p.placeID, p.text)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// placeDetails expands a prediction into address components.
func (g *GoogleAddressProvider) placeDetails(ctx context.Context, placeID, text string) (AddressSuggestion, error) {
	var resp struct {
		FormattedAddress  string `json:"formattedAddress"`
		AddressComponents []struct {
			LongText string   `json:"longText"`
			Types    []string `json:"types"`
		} `json:"addressComponents"`
	}
	endpoint := g.placesURL + "/v1/places/" + url.PathEscape(placeID)
	if err := g.get(ctx, endpoint, "formattedAddress,addressComponents", &resp); err != nil {
		return AddressSuggestion{}, fmt.Errorf("google place details: %w", err)
	}

	comp := func(typ string) string {
		for _, c := range resp.AddressComponents {
			for _, t := range c.Types {
				if t == typ {
					return c.LongText
				}
			}
		}
		return ""
	}
	line1 := strings.TrimSpace(comp("street_number") + " " + comp("route"))
	if line1 == "" {
		line1 = comp("premise")
	}
	city := comp("postal_town")
	if city == "" {
		city = comp("locality")
	}
	county := comp("administrative_area_level_2")
	if county == "" {
		county = comp("administrative_area_level_1")
	}
	return AddressSuggestion{
		PlaceID:          placeID,
		Description:      text,
		Line1:            line1,
		City:             city,
		StateOrCounty:    county,
		PostCode:         comp("postal_code"),
		FormattedAddress: resp.FormattedAddress,
	}, nil
}

// Verify checks an address with the Address Validation API. An address is
// verified only when Google considers it complete, resolved to a building or
// unit, and with no unconfirmed components.
func (g *GoogleAddressProvider) Verify(ctx context.Context, addr ProductAddress) (VerificationResult, error) {
	region, ok := googleRegion(addr.CountrySlug)
	if !ok || (addr.EntryMode == "search" && (addr.PlaceID == nil || *addr.PlaceID == "")) {
		return failedResult(FailureReasonNotVerifiable), nil
	}

	lines := []string{addr.Line1}
	if addr.Line2 != nil && strings.TrimSpace(*addr.Line2) != "" {
		lines = append(lines, *addr.Line2)
	}
	address := map[string]any{
		"addressLines":       lines,
		"locality":           addr.City,
		"administrativeArea": addr.StateOrCounty,
		"postalCode":         addr.PostCode,
	}
	if region != "" {
		address["regionCode"] = region
	}

	var resp struct {
		Result struct {
			Verdict struct {
				AddressComplete          bool   `json:"addressComplete"`
				HasUnconfirmedComponents bool   `json:"hasUnconfirmedComponents"`
				ValidationGranularity    string `json:"validationGranularity"`
			} `json:"verdict"`
		} `json:"result"`
	}
	payload := map[string]any{"address": address}
	if err := g.post(ctx, g.validationURL+"/v1:validateAddress", "", payload, &resp); err != nil {
		return VerificationResult{}, fmt.Errorf("google address validation: %w", err)
	}

	v := resp.Result.Verdict
	granular := v.ValidationGranularity == "PREMISE" || v.ValidationGranularity == "SUB_PREMISE"
	if v.AddressComplete && !v.HasUnconfirmedComponents && granular {
		return VerificationResult{Status: VerificationStatusVerified}, nil
	}
	return failedResult(FailureReasonInvalidAddress), nil
}

func failedResult(reason string) VerificationResult {
	return VerificationResult{Status: VerificationStatusFailed, FailureReason: &reason}
}

func euRegionCodes() []string {
	codes := make([]string, 0, len(googleRegionCodes))
	for slug, c := range googleRegionCodes {
		if slug != "united-kingdom" {
			codes = append(codes, c)
		}
	}
	return codes
}

// post sends a JSON request. fieldMask limits the fields Google returns and may be empty.
func (g *GoogleAddressProvider) post(ctx context.Context, endpoint, fieldMask string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", g.apiKey)
	if fieldMask != "" {
		req.Header.Set("X-Goog-FieldMask", fieldMask)
	}
	return g.do(ctx, req, out)
}

func (g *GoogleAddressProvider) get(ctx context.Context, endpoint, fieldMask string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("X-Goog-Api-Key", g.apiKey)
	req.Header.Set("X-Goog-FieldMask", fieldMask)
	return g.do(ctx, req, out)
}

func (g *GoogleAddressProvider) do(ctx context.Context, req *http.Request, out any) error {
	//nolint:gosec // endpoints are fixed Google hosts; the only variable part, a place ID, is path-escaped
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { besteffort.Log(ctx, "google response body close", resp.Body.Close()) }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, googleMaxBody))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.Unmarshal(data, out)
}
