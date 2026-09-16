package org

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// InviteTTL is how long a product organization invite remains valid.
const InviteTTL = 72 * time.Hour

// RoleOwner is the organization owner membership role.
const RoleOwner = "owner"

// RoleMember is a standard organization member role.
const RoleMember = "member"

// HashInviteToken returns a hex-encoded SHA-256 digest of the raw token.
func HashInviteToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewInviteToken generates a URL-safe opaque invite token.
func NewInviteToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate invite token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
