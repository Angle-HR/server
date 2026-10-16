// Package redislimit is the Redis-backed rate limiter of the application endpoint.
package redislimit

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Limiter counts events per key in a fixed window.
type Limiter struct {
	Client *goredis.Client
}

// New returns a limiter over client.
func New(client *goredis.Client) *Limiter { return &Limiter{Client: client} }

// Allow counts one event for key and reports whether it is within limit for the window. The key is created
// with its expiry first (SET NX EX), then incremented, so a crash between the two steps can never leave a
// counter that never expires.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	k := "ratelimit:" + key
	if err := l.Client.SetNX(ctx, k, 0, window).Err(); err != nil {
		return false, fmt.Errorf("redislimit: create %s: %w", key, err)
	}
	n, err := l.Client.Incr(ctx, k).Result()
	if err != nil {
		return false, fmt.Errorf("redislimit: count %s: %w", key, err)
	}
	return n <= int64(limit), nil
}
