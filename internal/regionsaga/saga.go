// Package regionsaga moves an onboarding account out of the global holding area into its country's regional
// database. It is a saga over two owners:
//
//	identity  the user row (accounts.pending_users -> regional accounts.users) and auth.users_registry
//	org       the organization draft and the onboarding progress
//
// The org service drives it (Coordinator) and calls identity over gRPC (IdentityService.PrepareRegionMove,
// CommitRegionMove, AbortRegionMove). Every step is idempotent and the state is persisted in
// accounts.region_move_sagas (migration global_registry/000018) after each step, so a crash, a timeout or a failed
// call resumes (Resumer) or compensates instead of leaving a half-moved account.
//
//	started            -> identity.Prepare      copy the user into the target region
//	identity_prepared  -> org.CopyPending       copy organization draft and progress
//	org_copied         -> identity.Commit       repoint users_registry, drop the pending user (point of no return)
//	identity_committed -> org.ReleasePending    drop the org side pending rows, emit org.region.moved.v1
//	completed
//
// A failure before the commit compensates in reverse (org.UndoCopy, identity.Abort) and ends in "compensated"; a
// failure after it only retries forward. The account is never unreachable: until the commit users_registry still
// points at the global holding area where the pending rows are still intact.
package regionsaga

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/region"
)

// States of a saga.
const (
	StateStarted           = "started"
	StateIdentityPrepared  = "identity_prepared"
	StateOrgCopied         = "org_copied"
	StateIdentityCommitted = "identity_committed"
	StateCompleted         = "completed"
	StateCompensating      = "compensating"
	StateCompensated       = "compensated"
)

// Saga is one persisted attempt to move a user.
type Saga struct {
	ID          uuid.UUID
	UserID      uuid.UUID
	Target      region.Region
	State       string
	Attempts    int
	LastError   string
	UpdatedAt   time.Time
	CompletedAt *time.Time
	CompanyID   string // set by the org copy step, informational
}

// Terminal reports whether the saga is over.
func (s Saga) Terminal() bool { return s.State == StateCompleted || s.State == StateCompensated }

// Errors.
var (
	// ErrConflict: the user already has a live move towards another region.
	ErrConflict = errors.New("regionsaga: the account is already being moved to another region")
	// ErrBusy: another worker holds the saga and did not finish in time; retry.
	ErrBusy = errors.New("regionsaga: the move is in progress")
	// ErrCompensated: the move failed before the commit and was rolled back; the account is unchanged.
	ErrCompensated = errors.New("regionsaga: the move failed and was rolled back")
)

// Permanent marks an error no retry can fix (for example a user that does not exist).
type Permanent struct{ Err error }

func (p Permanent) Error() string { return p.Err.Error() }
func (p Permanent) Unwrap() error { return p.Err }

// Store persists sagas.
type Store interface {
	// Begin returns the live saga of the user (same target), a completed one for the same target, or creates one.
	// A live saga towards another target is ErrConflict.
	Begin(ctx context.Context, userID uuid.UUID, target region.Region) (Saga, error)
	// Claim takes the saga for lease; false when another worker holds it or it is terminal.
	Claim(ctx context.Context, id uuid.UUID, lease time.Duration) (Saga, bool, error)
	// Advance moves the saga to state if it is in from (compare and set) and clears the error.
	Advance(ctx context.Context, id uuid.UUID, from, to string) error
	// Fail records the last error without changing the state.
	Fail(ctx context.Context, id uuid.UUID, msg string) error
	// Release gives up the lease.
	Release(ctx context.Context, id uuid.UUID) error
	// Get reads a saga.
	Get(ctx context.Context, id uuid.UUID) (Saga, error)
	// Resumable lists live sagas not touched since cutoff whose lease expired.
	Resumable(ctx context.Context, cutoff time.Time, limit int) ([]Saga, error)
}

// Identity is the identity service as the saga uses it.
type Identity interface {
	Prepare(ctx context.Context, userID uuid.UUID, target region.Region) error
	Commit(ctx context.Context, userID uuid.UUID, target region.Region) error
	Abort(ctx context.Context, userID uuid.UUID, target region.Region) error
}

// Org is the org side of the saga.
type Org interface {
	// CopyPending copies the pending organization draft and onboarding progress into the target region. It returns
	// the company id when an organization exists there.
	CopyPending(ctx context.Context, userID uuid.UUID, target region.Region) (companyID string, err error)
	// UndoCopy removes what CopyPending wrote; only valid before identity committed.
	UndoCopy(ctx context.Context, userID uuid.UUID, target region.Region) error
	// ReleasePending drops the org side pending rows.
	ReleasePending(ctx context.Context, userID uuid.UUID) error
	// Finalize emits org.region.moved.v1 (when ORG_EVENTS is on) in the target region. Idempotent.
	Finalize(ctx context.Context, s Saga) error
}

// Coordinator drives sagas.
type Coordinator struct {
	Store    Store
	Identity Identity
	Org      Org
	Log      *slog.Logger
	// Lease is how long a worker owns a saga (default 60s).
	Lease time.Duration
	// StepRetries is how many times a failing step is retried in place before the saga compensates or yields
	// (default 2); StepBackoff is the delay between tries (default 200ms).
	StepRetries int
	StepBackoff time.Duration
	// Wait is how long Move waits for a saga another worker is running (default 10s).
	Wait time.Duration
}

func (c *Coordinator) lease() time.Duration { return orDur(c.Lease, 60*time.Second) }
func (c *Coordinator) backoff() time.Duration {
	return orDur(c.StepBackoff, 200*time.Millisecond)
}
func (c *Coordinator) wait() time.Duration { return orDur(c.Wait, 10*time.Second) }
func (c *Coordinator) retries() int {
	if c.StepRetries < 0 {
		return 0
	}
	if c.StepRetries == 0 {
		return 2
	}
	return c.StepRetries
}
func (c *Coordinator) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

func orDur(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Move runs the saga for the user to completion and returns the target region. It is safe to call again after any
// failure or from several requests at once: a finished move answers immediately, a running one is waited for.
// ErrCompensated means the move failed before the commit and the account is unchanged.
func (c *Coordinator) Move(ctx context.Context, userID uuid.UUID, target region.Region) (region.Region, error) {
	if !region.Valid(target) || target == region.RegionGlobal {
		return region.RegionUnknown, fmt.Errorf("regionsaga: invalid migration target %q", target)
	}
	s, err := c.Store.Begin(ctx, userID, target)
	if err != nil {
		return region.RegionUnknown, err
	}
	if s.State == StateCompleted {
		return target, nil
	}
	// A saga found already unwinding is a failed attempt of an earlier request, not of this one: once this call has
	// finished the unwinding it starts a fresh attempt (once) instead of reporting the old failure.
	inherited := s.State == StateCompensating
	deadline := time.Now().Add(c.wait())
	for {
		done, derr := c.Drive(ctx, s.ID)
		if derr != nil {
			return region.RegionUnknown, derr
		}
		switch {
		case done.State == StateCompleted:
			return target, nil
		case done.State == StateCompensated && inherited:
			inherited = false
			if s, err = c.Store.Begin(ctx, userID, target); err != nil {
				return region.RegionUnknown, err
			}
			continue
		case done.State == StateCompensated:
			return region.RegionUnknown, fmt.Errorf("%w: %s", ErrCompensated, done.LastError)
		}
		// Another worker holds it: wait for it to finish.
		if time.Now().After(deadline) {
			return region.RegionUnknown, ErrBusy
		}
		select {
		case <-ctx.Done():
			return region.RegionUnknown, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		s = done
	}
}

// Drive claims the saga and runs it as far as it goes. When another worker holds the lease it returns the saga as
// stored, unchanged. A pre-commit failure compensates and returns the compensated saga with a nil error; a failure
// after the commit returns the error (the saga stays resumable).
func (c *Coordinator) Drive(ctx context.Context, id uuid.UUID) (Saga, error) {
	s, ok, err := c.Store.Claim(ctx, id, c.lease())
	if err != nil {
		return Saga{}, err
	}
	if !ok {
		return c.Store.Get(ctx, id)
	}
	// The lease is released even if the caller's context is gone, so a retry does not wait for it to lapse.
	defer func() { _ = c.Store.Release(context.WithoutCancel(ctx), id) }()
	for !s.Terminal() {
		next, serr := c.step(ctx, s)
		if serr != nil {
			return c.onFailure(ctx, s, serr)
		}
		s = next
	}
	return s, nil
}

// onFailure decides between rolling back and yielding.
func (c *Coordinator) onFailure(ctx context.Context, s Saga, cause error) (Saga, error) {
	bg := context.WithoutCancel(ctx)
	_ = c.Store.Fail(bg, s.ID, cause.Error())
	s.LastError = cause.Error()
	switch s.State {
	case StateStarted, StateIdentityPrepared, StateOrgCopied:
		c.log().Warn("region move failed before commit, compensating", "saga", s.ID, "user", s.UserID, "state", s.State, "error", cause)
		if err := c.Store.Advance(bg, s.ID, s.State, StateCompensating); err != nil {
			return s, fmt.Errorf("regionsaga: %w (after %v)", err, cause)
		}
		s.State = StateCompensating
		return c.compensate(bg, s)
	case StateCompensating:
		return s, fmt.Errorf("regionsaga: compensation of %s stalled: %w", s.ID, cause)
	default:
		// After the commit the account already lives in the target region: only retry forward.
		c.log().Error("region move failed after commit, will resume", "saga", s.ID, "user", s.UserID, "state", s.State, "error", cause)
		return s, fmt.Errorf("regionsaga: move %s is committed but not finished: %w", s.ID, cause)
	}
}

func (c *Coordinator) compensate(ctx context.Context, s Saga) (Saga, error) {
	if err := c.retry(ctx, func() error { return c.Org.UndoCopy(ctx, s.UserID, s.Target) }); err != nil {
		_ = c.Store.Fail(ctx, s.ID, "compensation: "+err.Error())
		return s, fmt.Errorf("regionsaga: compensate org: %w", err)
	}
	if err := c.retry(ctx, func() error { return c.Identity.Abort(ctx, s.UserID, s.Target) }); err != nil {
		_ = c.Store.Fail(ctx, s.ID, "compensation: "+err.Error())
		return s, fmt.Errorf("regionsaga: compensate identity: %w", err)
	}
	if err := c.Store.Advance(ctx, s.ID, StateCompensating, StateCompensated); err != nil {
		return s, err
	}
	s.State = StateCompensated
	return s, nil
}

// step runs the action of the saga's state and persists the transition.
func (c *Coordinator) step(ctx context.Context, s Saga) (Saga, error) {
	var (
		run func() error
		to  string
	)
	switch s.State {
	case StateStarted:
		run, to = func() error { return c.Identity.Prepare(ctx, s.UserID, s.Target) }, StateIdentityPrepared
	case StateIdentityPrepared:
		run = func() error {
			id, err := c.Org.CopyPending(ctx, s.UserID, s.Target)
			s.CompanyID = id
			return err
		}
		to = StateOrgCopied
	case StateOrgCopied:
		run, to = func() error { return c.Identity.Commit(ctx, s.UserID, s.Target) }, StateIdentityCommitted
	case StateIdentityCommitted:
		run = func() error {
			if err := c.Org.ReleasePending(ctx, s.UserID); err != nil {
				return err
			}
			return c.Org.Finalize(ctx, s)
		}
		to = StateCompleted
	case StateCompensating:
		return c.compensate(ctx, s)
	default:
		return s, fmt.Errorf("regionsaga: unknown state %q", s.State)
	}
	if err := c.retry(ctx, run); err != nil {
		return s, err
	}
	if err := c.Store.Advance(ctx, s.ID, s.State, to); err != nil {
		return s, err
	}
	s.State = to
	return s, nil
}

func (c *Coordinator) retry(ctx context.Context, f func() error) error {
	var err error
	for i := 0; ; i++ {
		if err = f(); err == nil {
			return nil
		}
		var perm Permanent
		if errors.As(err, &perm) || i >= c.retries() || ctx.Err() != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(c.backoff() * time.Duration(i+1)):
		}
	}
}

// ResumeOnce drives every live saga that stalled (crash, failed call after the commit) and returns how many it
// looked at.
func (c *Coordinator) ResumeOnce(ctx context.Context, idle time.Duration, limit int) (int, error) {
	list, err := c.Store.Resumable(ctx, time.Now().Add(-idle), limit)
	if err != nil {
		return 0, err
	}
	for _, s := range list {
		if _, derr := c.Drive(ctx, s.ID); derr != nil {
			c.log().Warn("region move resume failed", "saga", s.ID, "user", s.UserID, "state", s.State, "error", derr)
		}
	}
	return len(list), nil
}

// ResumeIntervalEnv overrides how often the resumer looks for stalled sagas (a Go duration, default 30s).
const ResumeIntervalEnv = "REGION_MOVE_RESUME_INTERVAL"

// RunResumer calls ResumeOnce every interval until ctx ends.
func (c *Coordinator) RunResumer(ctx context.Context, interval, idle time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := c.ResumeOnce(ctx, idle, 50); err != nil && ctx.Err() == nil {
				c.log().Warn("region move resumer", "error", err)
			}
		}
	}
}
