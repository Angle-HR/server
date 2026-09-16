package onboarding

import "context"

// AddressSuggestion is a candidate address from autocomplete search.
type AddressSuggestion struct {
	PlaceID          string
	Description      string
	Line1            string
	Line2            *string
	City             string
	StateOrCounty    string
	PostCode         string
	FormattedAddress string
}

// AddressSearcher returns address suggestions for a partial query.
type AddressSearcher interface {
	Search(ctx context.Context, countryID, query string) ([]AddressSuggestion, error)
}

// PassthroughAddressSearcher returns no suggestions until a real provider is wired.
// Intended for non-production via ADDRESS_SEARCH_MODE=passthrough.
type PassthroughAddressSearcher struct{}

// Search returns an empty suggestion list.
func (PassthroughAddressSearcher) Search(_ context.Context, _, _ string) ([]AddressSuggestion, error) {
	return []AddressSuggestion{}, nil
}
