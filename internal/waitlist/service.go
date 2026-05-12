package waitlist

import (
	"context"
	"fmt"

	"github.com/Angle-HR/server/pkg/apperror"
)

// JoinedPublisher enqueues follow-up work for new waitlist signups.
type JoinedPublisher interface {
	PublishWaitlistJoined(ctx context.Context, email string) error
}

// WaitlistService contains waitlist business logic.
//
//nolint:revive // Public API name matches the waitlist domain vocabulary.
type WaitlistService struct {
	repo      WaitlistRepository
	publisher JoinedPublisher
}

// NewWaitlistService returns a waitlist service backed by repo.
func NewWaitlistService(repo WaitlistRepository, publisher JoinedPublisher) *WaitlistService {
	return &WaitlistService{
		repo:      repo,
		publisher: publisher,
	}
}

// Join validates and stores a waitlist signup.
func (s *WaitlistService) Join(ctx context.Context, req JoinRequest) error {
	if err := req.Validate(); err != nil {
		return fmt.Errorf("validate join request: %w", apperror.ErrBadRequest)
	}

	exists, err := s.repo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return fmt.Errorf("check duplicate email: %w", err)
	}

	if exists {
		return fmt.Errorf("email already registered: %w", apperror.ErrConflict)
	}

	entry := WaitlistEntry{
		Email:       req.Email,
		CompanyName: req.CompanyName,
		CompanySize: req.CompanySize,
		Role:        req.Role,
	}

	if err := s.repo.Insert(ctx, entry); err != nil {
		return fmt.Errorf("insert waitlist entry: %w", err)
	}

	if s.publisher != nil {
		if err := s.publisher.PublishWaitlistJoined(ctx, req.Email); err != nil {
			return fmt.Errorf("publish waitlist joined task: %w", err)
		}
	}

	return nil
}

// Count returns the total number of waitlist signups.
func (s *WaitlistService) Count(ctx context.Context) (int64, error) {
	count, err := s.repo.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("count waitlist entries: %w", err)
	}

	return count, nil
}
