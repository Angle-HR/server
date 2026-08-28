package onboarding

import (
	"context"
	"strings"
)

const (
	VerificationStatusVerified   = "verified"
	VerificationStatusFailed     = "failed"
	VerificationStatusUnverified = "unverified"

	FailureReasonNotVerifiable  = "not_verifiable"
	FailureReasonInvalidAddress = "invalid_address"
)

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
	PlaceID            *string
	VerificationStatus string
}

// VerificationResult is returned by an address verifier.
type VerificationResult struct {
	Status        string
	FailureReason *string
}

// AddressVerifier checks an address payload.
type AddressVerifier interface {
	Verify(ctx context.Context, addr ProductAddress) (VerificationResult, error)
}

// PassthroughAddressVerifier marks any address as verified without a third-party call.
// Intended for non-production or interim use via ADDRESS_VERIFY_MODE=passthrough.
type PassthroughAddressVerifier struct{}

// Verify returns verified for any address with required fields present.
func (PassthroughAddressVerifier) Verify(_ context.Context, addr ProductAddress) (VerificationResult, error) {
	if addr.EntryMode == "search" && (addr.PlaceID == nil || *addr.PlaceID == "") {
		reason := FailureReasonNotVerifiable
		return VerificationResult{Status: VerificationStatusFailed, FailureReason: &reason}, nil
	}
	if strings.TrimSpace(addr.Line1) == "" || strings.TrimSpace(addr.City) == "" || strings.TrimSpace(addr.PostCode) == "" {
		reason := FailureReasonInvalidAddress
		return VerificationResult{Status: VerificationStatusFailed, FailureReason: &reason}, nil
	}
	return VerificationResult{Status: VerificationStatusVerified}, nil
}
