package auth

import (
	"errors"
	"unicode"
)

// Password length bounds shared by every endpoint that sets a password.
const (
	PasswordMinLength = 8
	PasswordMaxLength = 128
)

// PasswordPolicyMessage is the user-facing hint; it matches the client text.
const PasswordPolicyMessage = "Password must be at least 8 characters and include both letters and numbers."

// ErrPasswordPolicy is returned when a new password breaks the policy.
var ErrPasswordPolicy = errors.New(PasswordPolicyMessage)

// ValidatePassword enforces the password policy for newly chosen passwords:
// 8 to 128 characters with at least one letter and one digit. It must not be
// applied to login, so accounts created under older rules can still sign in.
func ValidatePassword(password string) error {
	n := len([]rune(password))
	if n < PasswordMinLength || n > PasswordMaxLength {
		return ErrPasswordPolicy
	}
	var letter, digit bool
	for _, r := range password {
		switch {
		case unicode.IsLetter(r):
			letter = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	if !letter || !digit {
		return ErrPasswordPolicy
	}
	return nil
}

// dummyPasswordHash is a valid bcrypt (cost 12) hash of a random value. It is
// compared against when no real hash exists so that lookups which found no
// account take about as long as ones that did.
const dummyPasswordHash = "$2a$12$C3lqNEC/uQqPs6XTZtXE/.5tburJ/njLNnonB5Dp4OvkLljx0L2Cq"

// DummyCheckPassword burns one bcrypt comparison against a fixed hash.
func DummyCheckPassword(password string) {
	_ = CheckPassword(dummyPasswordHash, password)
}
