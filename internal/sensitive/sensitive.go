// Package sensitive seals special category answers before they are written to the database. Each region has its
// own keys, so data of one region cannot be opened with the keys of another. Sealing uses AES-256-GCM with a
// random nonce, and the ciphertext is bound to the row it belongs to (the additional data), so it cannot be
// copied to another application or question.
//
// Keys come from the environment, one variable per region: ANGLEHR_<REGION>_SENSITIVE_KEYS, e.g.
//
//	ANGLEHR_UK_SENSITIVE_KEYS=v2:<base64 of 32 bytes>,v1:<base64 of 32 bytes>
//
// The first key is used to seal; every listed key can open, so a key is rotated by putting the new one first and
// keeping the old one until its rows are resealed or deleted by the retention task. A region with no keys cannot
// seal, and callers refuse the data instead of storing it unprotected.
package sensitive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Sealed is an encrypted value as stored.
type Sealed struct {
	KeyID      string
	Nonce      []byte
	Ciphertext []byte
}

// Errors.
var (
	// ErrNoKey means the region has no keys configured (or the key a row names is unknown).
	ErrNoKey = errors.New("sensitive: no key for this region")
	// ErrCannotOpen means the data was altered, or the additional data does not match the row.
	ErrCannotOpen = errors.New("sensitive: cannot open")
)

type ring struct {
	active string
	keys   map[string]cipher.AEAD
}

// Keyring holds the keys of every configured region. It is safe for concurrent use after loading.
type Keyring struct{ regions map[string]*ring }

// NewKeyring returns an empty keyring.
func NewKeyring() *Keyring { return &Keyring{regions: map[string]*ring{}} }

// Add configures a region from a spec of the form "id:base64,id:base64". The first key seals.
func (k *Keyring) Add(region, spec string) error {
	region = strings.ToLower(strings.TrimSpace(region))
	r := &ring{keys: map[string]cipher.AEAD{}}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, enc, ok := strings.Cut(part, ":")
		id = strings.TrimSpace(id)
		if !ok || id == "" || len(id) > 32 || strings.ContainsAny(id, " \t|") {
			return fmt.Errorf("sensitive: region %s: each key must look like id:base64", region)
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
		if err != nil {
			return fmt.Errorf("sensitive: region %s: key %s is not valid base64", region, id)
		}
		if len(raw) != 32 {
			return fmt.Errorf("sensitive: region %s: key %s must be 32 bytes, got %d", region, id, len(raw))
		}
		if _, dup := r.keys[id]; dup {
			return fmt.Errorf("sensitive: region %s: key id %s is listed twice", region, id)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return fmt.Errorf("sensitive: region %s: key %s: %w", region, id, err)
		}
		gcm, err := cipher.NewGCM(block)
		if err != nil {
			return fmt.Errorf("sensitive: region %s: key %s: %w", region, id, err)
		}
		if r.active == "" {
			r.active = id
		}
		r.keys[id] = gcm
	}
	if r.active == "" {
		return fmt.Errorf("sensitive: region %s: no keys given", region)
	}
	k.regions[region] = r
	return nil
}

// LoadFromEnv reads ANGLEHR_<REGION>_SENSITIVE_KEYS for each region. A region without the variable is left
// unconfigured. A variable that cannot be read is an error, so a typo is caught at start-up and never silently
// turns into "no encryption available".
func LoadFromEnv(regions []string, getenv func(string) string) (*Keyring, error) {
	k := NewKeyring()
	for _, reg := range regions {
		v := strings.TrimSpace(getenv("ANGLEHR_" + strings.ToUpper(reg) + "_SENSITIVE_KEYS"))
		if v == "" {
			continue
		}
		if err := k.Add(reg, v); err != nil {
			return nil, err
		}
	}
	return k, nil
}

// Has reports whether the region can seal.
func (k *Keyring) Has(region string) bool {
	if k == nil {
		return false
	}
	_, ok := k.regions[strings.ToLower(region)]
	return ok
}

// Seal encrypts plaintext with the region's active key. aad binds the value to its row.
func (k *Keyring) Seal(region string, plaintext []byte, aad string) (Sealed, error) {
	if k == nil {
		return Sealed{}, ErrNoKey
	}
	r := k.regions[strings.ToLower(region)]
	if r == nil {
		return Sealed{}, ErrNoKey
	}
	gcm := r.keys[r.active]
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("sensitive: nonce: %w", err)
	}
	return Sealed{KeyID: r.active, Nonce: nonce, Ciphertext: gcm.Seal(nil, nonce, plaintext, []byte(aad))}, nil
}

// Open decrypts a sealed value. It fails when the data or aad is not the one that was sealed.
func (k *Keyring) Open(region string, s Sealed, aad string) ([]byte, error) {
	if k == nil {
		return nil, ErrNoKey
	}
	r := k.regions[strings.ToLower(region)]
	if r == nil {
		return nil, ErrNoKey
	}
	gcm, ok := r.keys[s.KeyID]
	if !ok {
		return nil, ErrNoKey
	}
	if len(s.Nonce) != gcm.NonceSize() {
		return nil, ErrCannotOpen
	}
	out, err := gcm.Open(nil, s.Nonce, s.Ciphertext, []byte(aad))
	if err != nil {
		return nil, ErrCannotOpen
	}
	return out, nil
}
