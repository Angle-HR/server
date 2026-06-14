package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	verifyKeyPrefix         = "verify:"
	verifyResendPrefix      = "verify:resend:"
	verifyResendEmailPrefix = "verify:resend:email:"
	codeTTL                 = 300
	resendCooldownSeconds   = 30
	maxVerifyAttempts       = 5
)

// VerificationStore persists OTP sessions in Redis.
type VerificationStore struct {
	client *goredis.Client
}

// NewVerificationStore returns a Redis-backed verification store.
func NewVerificationStore(client *goredis.Client) *VerificationStore {
	return &VerificationStore{client: client}
}

// VerificationSession holds OTP state for an unverified signup.
type VerificationSession struct {
	SessionID string    `json:"session_id"`
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	Region    string    `json:"region"`
	Code      string    `json:"code"`
	Attempts  int       `json:"attempts"`
}

// CreateSession stores a new verification session and returns its public ID.
func (s *VerificationStore) CreateSession(ctx context.Context, session VerificationSession) error {
	if s == nil || s.client == nil {
		return errors.New("verification store unavailable")
	}

	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal verification session: %w", err)
	}

	key := verifyKeyPrefix + session.SessionID
	ok, err := s.client.SetNX(ctx, key, payload, time.Duration(codeTTL)*time.Second).Result()
	if err != nil {
		return fmt.Errorf("store verification session: %w", err)
	}
	if !ok {
		return errors.New("verification session already exists")
	}

	return nil
}

// ReplaceSession overwrites an existing session (email change / resend).
func (s *VerificationStore) ReplaceSession(ctx context.Context, session VerificationSession) error {
	if s == nil || s.client == nil {
		return errors.New("verification store unavailable")
	}

	payload, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("marshal verification session: %w", err)
	}

	key := verifyKeyPrefix + session.SessionID
	if err := s.client.Set(ctx, key, payload, time.Duration(codeTTL)*time.Second).Err(); err != nil {
		return fmt.Errorf("replace verification session: %w", err)
	}

	return nil
}

// GetSession loads a verification session.
func (s *VerificationStore) GetSession(ctx context.Context, sessionID string) (VerificationSession, error) {
	if s == nil || s.client == nil {
		return VerificationSession{}, errors.New("verification store unavailable")
	}

	raw, err := s.client.Get(ctx, verifyKeyPrefix+sessionID).Bytes()
	if errors.Is(err, goredis.Nil) {
		return VerificationSession{}, ErrVerificationExpired
	}
	if err != nil {
		return VerificationSession{}, fmt.Errorf("load verification session: %w", err)
	}

	var session VerificationSession
	if err := json.Unmarshal(raw, &session); err != nil {
		return VerificationSession{}, fmt.Errorf("parse verification session: %w", err)
	}

	session.SessionID = sessionID
	return session, nil
}

// DeleteSession removes a verification session.
func (s *VerificationStore) DeleteSession(ctx context.Context, sessionID string) error {
	if s == nil || s.client == nil {
		return nil
	}

	return s.client.Del(ctx, verifyKeyPrefix+sessionID).Err()
}

// ValidateCode checks the OTP and increments attempts on failure.
func (s *VerificationStore) ValidateCode(ctx context.Context, sessionID, code string) (VerificationSession, error) {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return VerificationSession{}, err
	}

	if session.Attempts >= maxVerifyAttempts {
		_ = s.DeleteSession(ctx, sessionID)
		return VerificationSession{}, ErrInvalidVerificationCode
	}

	if session.Code != code {
		session.Attempts++
		_ = s.ReplaceSession(ctx, session)
		return VerificationSession{}, ErrInvalidVerificationCode
	}

	if err := s.DeleteSession(ctx, sessionID); err != nil {
		return VerificationSession{}, err
	}

	return session, nil
}

// CanResend reports whether resend cooldown has elapsed.
func (s *VerificationStore) CanResend(ctx context.Context, sessionID string) (bool, error) {
	if s == nil || s.client == nil {
		return false, errors.New("verification store unavailable")
	}

	n, err := s.client.Exists(ctx, verifyResendPrefix+sessionID).Result()
	if err != nil {
		return false, err
	}

	return n == 0, nil
}

// MarkResent sets the resend cooldown marker.
func (s *VerificationStore) MarkResent(ctx context.Context, sessionID string) error {
	if s == nil || s.client == nil {
		return errors.New("verification store unavailable")
	}

	return s.client.Set(ctx, verifyResendPrefix+sessionID, "1", time.Duration(resendCooldownSeconds)*time.Second).Err()
}

// CanResendByEmail reports whether the email resend cooldown has elapsed.
func (s *VerificationStore) CanResendByEmail(ctx context.Context, email string) (bool, error) {
	if s == nil || s.client == nil {
		return false, errors.New("verification store unavailable")
	}

	n, err := s.client.Exists(ctx, verifyResendEmailPrefix+email).Result()
	if err != nil {
		return false, err
	}

	return n == 0, nil
}

// MarkResentByEmail sets the email resend cooldown marker.
func (s *VerificationStore) MarkResentByEmail(ctx context.Context, email string) error {
	if s == nil || s.client == nil {
		return errors.New("verification store unavailable")
	}

	return s.client.Set(ctx, verifyResendEmailPrefix+email, "1", time.Duration(resendCooldownSeconds)*time.Second).Err()
}

// CodeExpiresInSeconds returns the OTP TTL shown to clients.
func CodeExpiresInSeconds() int {
	return codeTTL
}

// ResendAvailableInSeconds returns the resend cooldown shown to clients.
func ResendAvailableInSeconds() int {
	return resendCooldownSeconds
}
