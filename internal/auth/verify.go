package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Angle-HR/server/pkg/besteffort"
)

const (
	verifyKeyPrefix         = "verify:"
	verifyResendPrefix      = "verify:resend:"
	verifyResendEmailPrefix = "verify:resend:email:"
	verifyLatestPrefix      = "verify:latest:"
	verifyAttemptsPrefix    = "verify:attempts:"
	codeTTL                 = 300
	resendCooldownSeconds   = 30
	maxVerifyAttempts       = 5

	// PurposeEmailVerify is signup / unverified-login email verification.
	PurposeEmailVerify = "email_verify"
	// PurposeLoginOTP is passwordless login OTP.
	PurposeLoginOTP = "login_otp"
)

// VerificationStore persists OTP sessions in Redis.
type VerificationStore struct {
	client *goredis.Client
}

// NewVerificationStore returns a Redis-backed verification store.
func NewVerificationStore(client *goredis.Client) *VerificationStore {
	return &VerificationStore{client: client}
}

// VerificationSession holds OTP state for email verification or login OTP.
type VerificationSession struct {
	SessionID string    `json:"session_id"`
	UserID    uuid.UUID `json:"user_id"`
	Email     string    `json:"email"`
	Region    string    `json:"region"`
	Code      string    `json:"code"`
	Attempts  int       `json:"attempts"`
	Purpose   string    `json:"purpose,omitempty"`
	// Decoy marks a session issued for an email that has no deliverable
	// account. No code was sent, and validation always fails (while still
	// counting attempts) so it is indistinguishable from a wrong code.
	Decoy bool `json:"decoy,omitempty"`
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
	// A replaced session carries a fresh code, so it gets a fresh attempt budget.
	if err := s.client.Del(ctx, verifyAttemptsPrefix+session.SessionID).Err(); err != nil {
		return fmt.Errorf("reset verification attempts: %w", err)
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

	return s.client.Del(ctx, verifyKeyPrefix+sessionID, verifyAttemptsPrefix+sessionID).Err()
}

// ValidateCode checks the OTP and increments attempts on failure.
func (s *VerificationStore) ValidateCode(ctx context.Context, sessionID, code string) (VerificationSession, error) {
	return s.ValidateCodeForPurpose(ctx, sessionID, code, "")
}

// ValidateCodeForPurpose checks the OTP and optionally enforces session purpose.
// Empty wantPurpose accepts email_verify sessions (including legacy sessions with empty purpose).
func (s *VerificationStore) ValidateCodeForPurpose(
	ctx context.Context,
	sessionID, code, wantPurpose string,
) (VerificationSession, error) {
	session, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return VerificationSession{}, err
	}

	purpose := session.Purpose
	if purpose == "" {
		purpose = PurposeEmailVerify
	}
	if wantPurpose == "" {
		wantPurpose = PurposeEmailVerify
	}
	if purpose != wantPurpose {
		return VerificationSession{}, ErrInvalidVerificationCode
	}

	// Count the attempt atomically before comparing. INCR is a single Redis
	// operation, so concurrent guesses can never share an attempt slot: at
	// most maxVerifyAttempts comparisons happen per session.
	attemptsKey := verifyAttemptsPrefix + sessionID
	n, err := s.client.Incr(ctx, attemptsKey).Result()
	if err != nil {
		return VerificationSession{}, fmt.Errorf("count verification attempt: %w", err)
	}
	if n == 1 {
		besteffort.Log(ctx, "client.Expire", s.client.Expire(ctx, attemptsKey, time.Duration(codeTTL)*time.Second).Err())
	}
	if n > maxVerifyAttempts {
		return VerificationSession{}, &VerifyAttemptsError{Locked: true, RetryAfter: s.lockRetryAfter(ctx, attemptsKey)}
	}

	if session.Decoy || subtle.ConstantTimeCompare([]byte(session.Code), []byte(code)) != 1 {
		return VerificationSession{}, &VerifyAttemptsError{Remaining: maxVerifyAttempts - int(n)}
	}

	// Deleting is the single-use gate: only the caller whose DEL removes the
	// session wins, so two parallel correct submissions cannot both succeed.
	removed, err := s.client.Del(ctx, verifyKeyPrefix+sessionID).Result()
	if err != nil {
		return VerificationSession{}, err
	}
	if removed == 0 {
		return VerificationSession{}, ErrVerificationExpired
	}
	besteffort.Log(ctx, "client.Del", s.client.Del(ctx, attemptsKey).Err())

	return session, nil
}

// lockRetryAfter is how long the attempt budget stays spent: the counter lives as long as the code does, so a
// fresh code (resend) is the way out sooner.
func (s *VerificationStore) lockRetryAfter(ctx context.Context, attemptsKey string) int {
	secs, err := s.retryAfter(ctx, attemptsKey)
	if err != nil || secs < 1 {
		return codeTTL
	}
	return secs
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

// RetryAfterBySession returns the seconds left on the per-session resend
// cooldown, or 0 when a resend is allowed.
func (s *VerificationStore) RetryAfterBySession(ctx context.Context, sessionID string) (int, error) {
	return s.retryAfter(ctx, verifyResendPrefix+sessionID)
}

// RetryAfterByEmail returns the seconds left on the per-email resend cooldown,
// or 0 when a new code may be requested.
func (s *VerificationStore) RetryAfterByEmail(ctx context.Context, email string) (int, error) {
	return s.retryAfter(ctx, verifyResendEmailPrefix+email)
}

func (s *VerificationStore) retryAfter(ctx context.Context, key string) (int, error) {
	if s == nil || s.client == nil {
		return 0, errors.New("verification store unavailable")
	}
	ttl, err := s.client.TTL(ctx, key).Result()
	if err != nil {
		return 0, err
	}
	if ttl <= 0 {
		return 0, nil
	}
	secs := int((ttl + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, nil
}

func latestKey(purpose, email string) string {
	return verifyLatestPrefix + purpose + ":" + email
}

// ReplaceLatestSession records sessionID as the only live session for the email
// and purpose, deleting the previously recorded one so its code stops working.
func (s *VerificationStore) ReplaceLatestSession(ctx context.Context, purpose, email, sessionID string) error {
	if s == nil || s.client == nil {
		return errors.New("verification store unavailable")
	}
	key := latestKey(purpose, email)
	prev, err := s.client.GetSet(ctx, key, sessionID).Result()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}
	if err := s.client.Expire(ctx, key, time.Duration(codeTTL)*time.Second).Err(); err != nil {
		return err
	}
	if prev != "" && prev != sessionID {
		return s.DeleteSession(ctx, prev)
	}
	return nil
}

// LatestSessionID returns the live session recorded for the email and purpose,
// or "" when none exists or it has expired.
func (s *VerificationStore) LatestSessionID(ctx context.Context, purpose, email string) (string, error) {
	if s == nil || s.client == nil {
		return "", errors.New("verification store unavailable")
	}
	id, err := s.client.Get(ctx, latestKey(purpose, email)).Result()
	if errors.Is(err, goredis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	n, err := s.client.Exists(ctx, verifyKeyPrefix+id).Result()
	if err != nil || n == 0 {
		return "", err
	}
	return id, nil
}
