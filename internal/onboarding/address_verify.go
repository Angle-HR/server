package onboarding

import "context"

// ProductAddress is the subset of address fields used for verification.
type ProductAddress struct {
	CountryID          string
	EntryMode          string
	Line1              string
	Line2              *string
	City               string
	StateOrCounty      string
	PostCode           string
	FormattedAddress   *string
	VerificationStatus string
}

// AddressVerifier checks a saved address.
type AddressVerifier interface {
	Verify(ctx context.Context, addr ProductAddress) (status string, err error)
}

// PassthroughAddressVerifier marks a saved address as verified without a third-party call.
// Intended for non-production or interim use via ADDRESS_VERIFY_MODE=passthrough.
type PassthroughAddressVerifier struct{}

// Verify returns "verified" for any saved address.
func (PassthroughAddressVerifier) Verify(_ context.Context, _ ProductAddress) (string, error) {
	return "verified", nil
}
