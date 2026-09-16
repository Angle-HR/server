package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

const totpIssuer = "OpenHR"

// TOTPCrypto encrypts authenticator secrets at rest.
type TOTPCrypto struct {
	gcm cipher.AEAD
}

// NewTOTPCrypto derives an AES-GCM key from the provided secret material.
func NewTOTPCrypto(secretMaterial string) (*TOTPCrypto, error) {
	if secretMaterial == "" {
		return nil, errors.New("totp encryption key is required")
	}
	sum := sha256.Sum256([]byte(secretMaterial))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &TOTPCrypto{gcm: gcm}, nil
}

// Encrypt encodes plaintext with AES-GCM and returns a base64 payload.
func (c *TOTPCrypto) Encrypt(plaintext string) (string, error) {
	if c == nil || c.gcm == nil {
		return "", errors.New("totp crypto unavailable")
	}
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := c.gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// Decrypt reverses Encrypt.
func (c *TOTPCrypto) Decrypt(encoded string) (string, error) {
	if c == nil || c.gcm == nil {
		return "", errors.New("totp crypto unavailable")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode totp secret: %w", err)
	}
	nonceSize := c.gcm.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("invalid totp secret payload")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plain, err := c.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt totp secret: %w", err)
	}
	return string(plain), nil
}

// GenerateTOTP creates a new TOTP key for email.
func GenerateTOTP(email string) (*otp.Key, error) {
	return totp.Generate(totp.GenerateOpts{
		Issuer:      totpIssuer,
		AccountName: email,
	})
}

// ValidateTOTP checks a 6-digit code against the secret.
func ValidateTOTP(secret, code string) bool {
	return totp.Validate(code, secret)
}
