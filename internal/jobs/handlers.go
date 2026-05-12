package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"
)

// Register mounts job handlers on mux.
func Register(mux *asynq.ServeMux, log *slog.Logger) {
	mux.HandleFunc(TypeWaitlistJoined, handleWaitlistJoined(log))
}

func handleWaitlistJoined(log *slog.Logger) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload WaitlistJoinedPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("decode waitlist joined payload: %w", err)
		}

		log.InfoContext(ctx, "processed waitlist joined task", "email", payload.Email)
		return nil
	}
}
