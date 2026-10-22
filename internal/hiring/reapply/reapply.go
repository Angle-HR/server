// Package reapply makes the keyed hash that identifies a candidate in a reapply block. The block table holds this
// hash and never the address, so a copy of the table does not reveal who was turned down. The hash covers the
// company too, so the same address gives a different value for each company and blocks cannot be matched across
// companies.
package reapply

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// MinKeyLength is the shortest key accepted.
const MinKeyLength = 32

// Hasher computes the hash. Its zero value, and a nil *Hasher, have no key and report Enabled false.
type Hasher struct{ key []byte }

// New returns a hasher for key, which must be at least MinKeyLength bytes (a long random string).
func New(key string) (*Hasher, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return &Hasher{}, nil
	}
	if len(key) < MinKeyLength {
		return nil, errors.New("reapply: the key must be at least 32 characters")
	}
	return &Hasher{key: []byte(key)}, nil
}

// Enabled reports whether a key is set.
func (h *Hasher) Enabled() bool { return h != nil && len(h.key) > 0 }

// Hash is the lower-case hex HMAC-SHA256 of the company and the normalised address.
func (h *Hasher) Hash(tenantID, email string) string {
	if !h.Enabled() {
		return ""
	}
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte(tenantID))
	m.Write([]byte{0})
	m.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(m.Sum(nil))
}
