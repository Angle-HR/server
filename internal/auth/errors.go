package auth

import "errors"

var (
	// ErrInvalidVerificationCode indicates a wrong OTP.
	ErrInvalidVerificationCode = errors.New("invalid verification code")
	// ErrVerificationExpired indicates the OTP TTL elapsed.
	ErrVerificationExpired = errors.New("verification expired")
	// ErrVerificationRateLimited indicates resend was requested too soon.
	ErrVerificationRateLimited = errors.New("verification rate limited")
)

// VerifyAttemptsError is returned by ValidateCode for a wrong code (or a spent attempt budget). It matches
// ErrInvalidVerificationCode with errors.Is, so callers that only care about "wrong code" keep working.
type VerifyAttemptsError struct {
	// Remaining is how many more attempts the code allows (0 when none are left).
	Remaining int
	// Locked is true when the attempt budget was already spent before this attempt: the code is no longer
	// compared and a fresh code (resend) is required.
	Locked bool
	// RetryAfter is, when Locked, the seconds until the counter expires.
	RetryAfter int
}

func (e *VerifyAttemptsError) Error() string { return ErrInvalidVerificationCode.Error() }

// Is makes errors.Is(err, ErrInvalidVerificationCode) true.
func (e *VerifyAttemptsError) Is(target error) bool { return target == ErrInvalidVerificationCode }
