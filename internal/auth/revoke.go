package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

const (
	revokeJTIPrefix  = "auth:revoke:"
	revokeUserPrefix = "auth:user_revoke:"
)

// RevocationStore tracks revoked refresh tokens and per-user session invalidation.
type RevocationStore struct {
	client *goredis.Client
}

// NewRevocationStore returns a Redis-backed revocation store.
func NewRevocationStore(client *goredis.Client) *RevocationStore {
	return &RevocationStore{client: client}
}

// RevokeJTI denylists a refresh token ID until expiresAt.
func (s *RevocationStore) RevokeJTI(ctx context.Context, jti string, expiresAt time.Time) error {
	if s == nil || s.client == nil {
		return errors.New("revocation store unavailable")
	}
	if jti == "" {
		return nil
	}

	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil
	}

	return s.client.Set(ctx, revokeJTIPrefix+jti, "1", ttl).Err()
}

// IsJTIRevoked reports whether a refresh jti has been denylisted.
func (s *RevocationStore) IsJTIRevoked(ctx context.Context, jti string) (bool, error) {
	if s == nil || s.client == nil {
		return false, errors.New("revocation store unavailable")
	}
	if jti == "" {
		return false, nil
	}

	n, err := s.client.Exists(ctx, revokeJTIPrefix+jti).Result()
	if err != nil {
		return false, fmt.Errorf("check revoked jti: %w", err)
	}
	return n > 0, nil
}

// RevokeUserSessions invalidates all refresh tokens issued for userID at or before now.
func (s *RevocationStore) RevokeUserSessions(ctx context.Context, userID uuid.UUID, refreshTTL time.Duration) error {
	if s == nil || s.client == nil {
		return errors.New("revocation store unavailable")
	}

	now := time.Now().UTC().Unix()
	key := revokeUserPrefix + userID.String()
	ttl := refreshTTL
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return s.client.Set(ctx, key, strconv.FormatInt(now, 10), ttl).Err()
}

// UserSessionsRevokedAt returns the unix timestamp after which refresh tokens for the user are invalid.
// Returns zero if no user-level revocation is set.
func (s *RevocationStore) UserSessionsRevokedAt(ctx context.Context, userID uuid.UUID) (int64, error) {
	if s == nil || s.client == nil {
		return 0, errors.New("revocation store unavailable")
	}

	raw, err := s.client.Get(ctx, revokeUserPrefix+userID.String()).Result()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("load user revoke: %w", err)
	}

	ts, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, nil
	}
	return ts, nil
}

// IsRefreshValid checks jti denylist and optional user-level revocation against claims issued-at.
func (s *RevocationStore) IsRefreshValid(ctx context.Context, claims RefreshClaims) (bool, error) {
	if claims.ID != "" {
		revoked, err := s.IsJTIRevoked(ctx, claims.ID)
		if err != nil {
			return false, err
		}
		if revoked {
			return false, nil
		}
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return false, nil
	}

	revokedAt, err := s.UserSessionsRevokedAt(ctx, userID)
	if err != nil {
		return false, err
	}
	if revokedAt == 0 {
		return true, nil
	}

	var iat int64
	if claims.IssuedAt != nil {
		iat = claims.IssuedAt.Unix()
	}
	return iat > revokedAt, nil
}
