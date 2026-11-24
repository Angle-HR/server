package auth

import (
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
)

// NewTOTPCryptoKeys builds a TOTPCrypto that encrypts with current and can still decrypt payloads written under any
// of the previous keys (TOTP_ENCRYPTION_KEY_PREVIOUS). Empty previous entries are ignored.
func NewTOTPCryptoKeys(current string, previous []string) (*TOTPCrypto, error) {
	gcm, err := newGCM(current)
	if err != nil {
		return nil, err
	}
	c := &TOTPCrypto{gcm: gcm}
	for _, p := range previous {
		if p == "" {
			continue
		}
		prev, prevErr := newGCM(p)
		if prevErr != nil {
			return nil, prevErr
		}
		c.previous = append(c.previous, cipher.AEAD(prev))
	}
	return c, nil
}

// Reencrypt returns encoded re-encrypted under the current key. changed is false when encoded already uses the
// current key (the original string is returned untouched). It fails when no configured key opens the payload.
func (c *TOTPCrypto) Reencrypt(encoded string) (out string, changed bool, err error) {
	if c == nil || c.gcm == nil {
		return "", false, errors.New("totp crypto unavailable")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false, fmt.Errorf("decode totp secret: %w", err)
	}
	plain, idx, err := c.open(raw)
	if err != nil {
		return "", false, err
	}
	if idx == 0 {
		return encoded, false, nil
	}
	out, err = c.Encrypt(plain)
	if err != nil {
		return "", false, err
	}
	return out, true, nil
}
