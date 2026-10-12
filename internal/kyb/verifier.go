package kyb

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Outcome is what a verifier concluded about a submission.
type Outcome string

const (
	// OutcomeVerified means the registry record matches.
	OutcomeVerified Outcome = "verified"
	// OutcomeNeedsReview means no automated check exists; a person must check the
	// government portal (Tier 2). This is not a failure.
	OutcomeNeedsReview Outcome = "needs_review"
	// OutcomeFailed means the check ran and did not match; Reason says why.
	OutcomeFailed Outcome = "failed"
)

// Request is what a verifier checks.
type Request struct {
	CountryCode        string
	RegistrationNumber string
	LegalName          string
	Address            Address
	Identifiers        map[string]string
}

// Result is a verifier's conclusion. RegistryName and RegisteredAddress are the
// values the registry holds, shown back to the user on a mismatch.
type Result struct {
	Outcome           Outcome
	Reason            FailureReason
	RegistryName      string
	RegisteredAddress string
}

// Verifier checks a submission against one registry. A returned error means the
// check could not run (network, rate limit, bad credentials): it is not a
// verification failure and must not change the organization's status.
type Verifier interface {
	Verify(ctx context.Context, req *Request) (Result, error)
}

// Errors a verifier can return when the check could not run.
var (
	ErrRegistryUnavailable = errors.New("kyb: registry unavailable")
	ErrRateLimited         = errors.New("kyb: registry rate limited")
	ErrRegistryAuth        = errors.New("kyb: registry credentials rejected")
)

// Tier is the execution tier from KYB V2 §4.1. Tier 3 (paid) is not built.
const (
	Tier1 = 1 // free official API, automated at signup
	Tier2 = 2 // free manual portal, operator review
)

// Registry maps a country to its automated verifier. Countries with none are Tier 2.
type Registry struct {
	mu        sync.RWMutex
	verifiers map[string]Verifier
}

// NewRegistry returns an empty registry: every country is Tier 2 until a verifier is registered.
func NewRegistry() *Registry { return &Registry{verifiers: map[string]Verifier{}} }

// Register installs a Tier 1 verifier for an ISO country code.
func (r *Registry) Register(countryCode string, v Verifier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.verifiers[strings.ToUpper(strings.TrimSpace(countryCode))] = v
}

// TierFor returns 1 when an automated verifier exists for the country, otherwise 2.
func (r *Registry) TierFor(countryCode string) int {
	if _, ok := r.lookup(countryCode); ok {
		return Tier1
	}
	return Tier2
}

func (r *Registry) lookup(countryCode string) (Verifier, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.verifiers[strings.ToUpper(strings.TrimSpace(countryCode))]
	return v, ok
}

// Verify runs the country's verifier, or returns OutcomeNeedsReview for Tier 2 countries.
func (r *Registry) Verify(ctx context.Context, req *Request) (Result, int, error) {
	v, ok := r.lookup(req.CountryCode)
	if !ok {
		return Result{Outcome: OutcomeNeedsReview}, Tier2, nil
	}
	res, err := v.Verify(ctx, req)
	return res, Tier1, err
}
