// Package kyb implements company verification (KYB): confirming that a
// business is a real, registered and active entity before it can publish jobs.
// It follows the KYB V2 doc. The package has no database or HTTP server
// dependencies; persistence, the review queue and notifications are interfaces
// that the handler layer implements.
package kyb

import "time"

// Status is the stored verification status of an organization
// (accounts.organizations.kyb_status).
type Status string

// Stored verification statuses.
const (
	StatusNotStarted Status = "not_started"
	StatusPending    Status = "pending"
	StatusVerified   Status = "verified"
	StatusFailed     Status = "failed"
)

// Valid reports whether s is one of the four stored statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusNotStarted, StatusPending, StatusVerified, StatusFailed:
		return true
	}
	return false
}

// CanDraft is always true: drafting is never blocked (KYB V2 §5.1).
func (s Status) CanDraft() bool { return true }

// CanPublish is true only for verified organizations (KYB V2 §5.1).
func (s Status) CanPublish() bool { return s == StatusVerified }

// FailureReason is why a check failed. The values match the CHECK constraint
// on accounts.organization_verifications.failure_reason.
type FailureReason string

// Failure reasons, matching the CHECK constraint on failure_reason.
const (
	ReasonNumberNotFound  FailureReason = "number_not_found"
	ReasonNameMismatch    FailureReason = "name_mismatch"
	ReasonAddressMismatch FailureReason = "address_mismatch"
	ReasonInactiveEntity  FailureReason = "inactive_entity"
	ReasonWrongCountry    FailureReason = "wrong_country"
)

// Valid reports whether r is a known failure reason.
func (r FailureReason) Valid() bool {
	switch r {
	case ReasonNumberNotFound, ReasonNameMismatch, ReasonAddressMismatch, ReasonInactiveEntity, ReasonWrongCountry:
		return true
	}
	return false
}

// HardBlock is true when no self-serve retry is allowed: the registry shows the
// entity as dissolved, inactive or insolvent (KYB V2 §5.2).
func (r FailureReason) HardBlock() bool { return r == ReasonInactiveEntity }

// SoftWarning is true when the user can accept the result by confirming which
// address type they entered (KYB V2 §5.2).
func (r FailureReason) SoftWarning() bool { return r == ReasonAddressMismatch }

// LightRetry is true for the two fixable cases that use the short re-verification
// form of company name and registration number only (KYB V2 §5.3).
func (r FailureReason) LightRetry() bool {
	return r == ReasonNumberNotFound || r == ReasonNameMismatch
}

// Display is the badge the dashboard shows (KYB V2 §5.5). It is derived, not stored.
type Display string

// Dashboard badges.
const (
	DisplayVerified       Display = "verified"        // green
	DisplayPendingReview  Display = "pending_review"  // amber
	DisplayActionRequired Display = "action_required" // orange
	DisplayFailed         Display = "failed"          // red, dissolved-entity case
	DisplayNotStarted     Display = "not_started"     // grey
)

// DisplayState derives the dashboard badge from the stored status and reason.
func DisplayState(s Status, reason FailureReason) Display {
	switch s {
	case StatusVerified:
		return DisplayVerified
	case StatusPending:
		return DisplayPendingReview
	case StatusFailed:
		if reason.HardBlock() {
			return DisplayFailed
		}
		return DisplayActionRequired
	case StatusNotStarted:
		return DisplayNotStarted
	default:
		return DisplayNotStarted
	}
}

// AddressType is which address the user entered (accounts.organization_verifications.address_type).
type AddressType string

// Address types a user can confirm.
const (
	AddressRegistered AddressType = "registered"
	AddressTrading    AddressType = "trading"
)

// Valid reports whether t is a known address type.
func (t AddressType) Valid() bool { return t == AddressRegistered || t == AddressTrading }

// Address is a postal address as submitted or as held by the registry.
type Address struct {
	Line1    string
	Line2    string
	City     string
	Region   string
	PostCode string
	Country  string
}

// Record is the persisted verification detail for one organization
// (accounts.organization_verifications).
type Record struct {
	OrganizationID     string
	CountryCode        string // ISO 3166-1 alpha-2, upper case
	RegistrationNumber string
	Identifiers        map[string]string
	LegalName          string // legal_name_submitted
	Address            Address
	AddressType        AddressType
	Tier               int
	Status             Status
	FailureReason      FailureReason
	RegistryName       string
	RegisteredAddress  string
	Attempts           int
	CheckedAt          *time.Time
	VerifiedAt         *time.Time
	ReviewerID         string
	NoticesSent        int // re-engagement nudges already sent for the current failure
}
