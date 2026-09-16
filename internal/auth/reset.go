package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	resetKeyPrefix = "auth:reset:"
	resetTTL       = time.Hour
)

// ResetStore persists password-reset tokens in Redis.
type ResetStore struct {
	client *goredis.Client
}

// NewResetStore returns a Redis-backed password reset store.
func NewResetStore(client *goredis.Client) *ResetStore {
	return &ResetStore{client: client}
}

// ResetSession binds a reset token to a user.
type ResetSession struct {
	UserID uuid.UUID `json:"user_id"`
	Email  string    `json:"email"`
	Region string    `json:"region"`
}

// CreateToken stores a new opaque reset token and returns the raw token.
func (s *ResetStore) CreateToken(ctx context.Context, session ResetSession) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("reset store unavailable")
	}

	raw, err := newOpaqueToken()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("marshal reset session: %w", err)
	}

	if err := s.client.Set(ctx, resetKeyPrefix+raw, payload, resetTTL).Err(); err != nil {
		return "", fmt.Errorf("store reset token: %w", err)
	}

	return raw, nil
}

// ConsumeToken loads and deletes a reset token.
func (s *ResetStore) ConsumeToken(ctx context.Context, token string) (ResetSession, error) {
	if s == nil || s.client == nil {
		return ResetSession{}, errors.New("reset store unavailable")
	}
	if token == "" {
		return ResetSession{}, ErrVerificationExpired
	}

	key := resetKeyPrefix + token
	raw, err := s.client.GetDel(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return ResetSession{}, ErrVerificationExpired
	}
	if err != nil {
		return ResetSession{}, fmt.Errorf("load reset token: %w", err)
	}

	var session ResetSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return ResetSession{}, fmt.Errorf("parse reset session: %w", err)
	}
	return session, nil
}

// ResetTTLSeconds returns the reset token lifetime shown to clients / emails.
func ResetTTLSeconds() int {
	return int(resetTTL.Seconds())
}

func newOpaqueToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
