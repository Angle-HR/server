// Package queue provides helpers for Asynq task queue connections.
package queue

import (
	"fmt"

	"github.com/hibiken/asynq"
)

// ConnOpt parses redisURL into Asynq Redis connection options.
func ConnOpt(redisURL string) (asynq.RedisConnOpt, error) {
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	return opt, nil
}

// NewClient creates an Asynq client for the Redis instance at redisURL.
func NewClient(redisURL string) (*asynq.Client, error) {
	opt, err := ConnOpt(redisURL)
	if err != nil {
		return nil, err
	}

	return asynq.NewClient(opt), nil
}

// NewServer creates an Asynq worker server for the Redis instance at redisURL.
func NewServer(redisURL string, cfg *asynq.Config) (*asynq.Server, error) {
	opt, err := ConnOpt(redisURL)
	if err != nil {
		return nil, err
	}

	return asynq.NewServer(opt, *cfg), nil
}
