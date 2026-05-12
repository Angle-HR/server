package jobs

import (
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"
)

const (
	// TypeWaitlistJoined identifies waitlist signup follow-up tasks.
	TypeWaitlistJoined = "waitlist:joined"
)

// WaitlistJoinedPayload is the payload for waitlist joined tasks.
type WaitlistJoinedPayload struct {
	Email string `json:"email"`
}

// NewWaitlistJoinedTask builds a task for a new waitlist signup.
func NewWaitlistJoinedTask(email string) (*asynq.Task, error) {
	payload, err := json.Marshal(WaitlistJoinedPayload{Email: email})
	if err != nil {
		return nil, fmt.Errorf("marshal waitlist joined payload: %w", err)
	}

	return asynq.NewTask(TypeWaitlistJoined, payload), nil
}
