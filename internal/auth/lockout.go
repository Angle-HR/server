package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// ErrTooManyAttempts indicates a key is currently locked out after repeated failures.
var ErrTooManyAttempts = errors.New("too many failed attempts")

// LoginLockout tracks failed authentication attempts per key (an email, a
// user ID, etc.) and locks the key out for a period once a threshold of
// failures is reached within a rolling window. Used to blunt password and
// TOTP brute-force attempts that a single OTP-style attempt counter doesn't
// cover.
type LoginLockout struct {
	client       *goredis.Client
	prefix       string
	maxAttempts  int64
	window       time.Duration
	lockDuration time.Duration
}

// NewLoginLockout returns a lockout tracker namespaced by prefix so
// different call sites (product login, admin login, TOTP checks, ...) don't
// share counters. maxAttempts failures within window trigger a lockDuration
// lockout.
func NewLoginLockout(client *goredis.Client, prefix string, maxAttempts int, window, lockDuration time.Duration) *LoginLockout {
	return &LoginLockout{
		client:       client,
		prefix:       prefix,
		maxAttempts:  int64(maxAttempts),
		window:       window,
		lockDuration: lockDuration,
	}
}

func (l *LoginLockout) attemptsKey(key string) string {
	return fmt.Sprintf("auth:lockout:%s:attempts:%s", l.prefix, key)
}

func (l *LoginLockout) lockKey(key string) string {
	return fmt.Sprintf("auth:lockout:%s:locked:%s", l.prefix, key)
}

// Check returns ErrTooManyAttempts if key is currently locked out. A nil
// receiver, nil client, or empty key is treated as "not locked" so callers
// stay safe if Redis is unavailable or lockout wasn't configured.
func (l *LoginLockout) Check(ctx context.Context, key string) error {
	if l == nil || l.client == nil || key == "" {
		return nil
	}

	n, err := l.client.Exists(ctx, l.lockKey(key)).Result()
	if err != nil {
		return fmt.Errorf("check lockout: %w", err)
	}
	if n > 0 {
		return ErrTooManyAttempts
	}
	return nil
}

// RecordFailure increments the failure counter for key and locks it out once
// maxAttempts is reached within window. Returns true if this call triggered
// the lockout.
func (l *LoginLockout) RecordFailure(ctx context.Context, key string) (bool, error) {
	if l == nil || l.client == nil || key == "" {
		return false, nil
	}

	attemptsKey := l.attemptsKey(key)
	n, err := l.client.Incr(ctx, attemptsKey).Result()
	if err != nil {
		return false, fmt.Errorf("record failed attempt: %w", err)
	}
	if n == 1 {
		if err := l.client.Expire(ctx, attemptsKey, l.window).Err(); err != nil {
			return false, fmt.Errorf("set attempt window: %w", err)
		}
	}

	if n < l.maxAttempts {
		return false, nil
	}

	if err := l.client.Set(ctx, l.lockKey(key), "1", l.lockDuration).Err(); err != nil {
		return false, fmt.Errorf("set lockout: %w", err)
	}
	_ = l.client.Del(ctx, attemptsKey)
	return true, nil
}

// Reset clears the failure counter and any active lockout for key. Call on
// successful authentication.
func (l *LoginLockout) Reset(ctx context.Context, key string) error {
	if l == nil || l.client == nil || key == "" {
		return nil
	}
	return l.client.Del(ctx, l.attemptsKey(key), l.lockKey(key)).Err()
}
