package jobs

import (
	"context"
	"fmt"

	"github.com/hibiken/asynq"
)

// Publisher enqueues background jobs.
type Publisher struct {
	client *asynq.Client
}

// NewPublisher returns a publisher backed by client.
func NewPublisher(client *asynq.Client) *Publisher {
	return &Publisher{client: client}
}

// PublishWaitlistJoined enqueues a waitlist joined task.
func (p *Publisher) PublishWaitlistJoined(ctx context.Context, email string) error {
	task, err := NewWaitlistJoinedTask(email)
	if err != nil {
		return err
	}

	if _, err := p.client.EnqueueContext(ctx, task); err != nil {
		return fmt.Errorf("enqueue waitlist joined task: %w", err)
	}

	return nil
}
