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
