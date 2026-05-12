package db

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// NewRedis creates and verifies a Redis client.
func NewRedis(ctx context.Context, redisURL string) (*redis.Client, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	client := redis.NewClient(opts)
	if err := client.Ping(ctx).Err(); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			return nil, fmt.Errorf("ping redis: %w (close client: %v)", err, closeErr)
		}

		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
