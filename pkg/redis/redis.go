// Package redis provides a Redis client helper.
package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

const defaultDialTimeout = 5 * time.Second

// NewClient parses redisURL, connects, and verifies connectivity with PING.
func NewClient(ctx context.Context, redisURL string) (*goredis.Client, error) {
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis config: %w", err)
	}

	if opts.DialTimeout == 0 {
		opts.DialTimeout = defaultDialTimeout
	}

	client := goredis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
