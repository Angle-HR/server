package regionsaga

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment variables.
const (
	// EnvMode selects how onboarding moves an account out of the holding area: "inline" (default, the code that ran
	// inside the handler) or "saga".
	EnvMode = "REGION_MOVE"
	// EnvIdentityAddr is identity's gRPC address; with it the saga calls IdentityService, without it identity's half
	// runs in this process over the shared databases (the monolith).
	EnvIdentityAddr = "IDENTITY_GRPC_ADDR"
)

// SagaEnabled reports REGION_MOVE=saga.
func SagaEnabled() bool { return strings.EqualFold(strings.TrimSpace(os.Getenv(EnvMode)), "saga") }

// GRPCIdentity calls identity's IdentityService.
type GRPCIdentity struct {
	Client identityv1.IdentityServiceClient
}

var _ Identity = (*GRPCIdentity)(nil)

// Prepare implements Identity.
func (g *GRPCIdentity) Prepare(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := g.Client.PrepareRegionMove(ctx, &identityv1.PrepareRegionMoveRequest{UserId: userID.String(), TargetRegion: string(target)})
	return fromGRPC("prepare", err)
}

// Commit implements Identity.
func (g *GRPCIdentity) Commit(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := g.Client.CommitRegionMove(ctx, &identityv1.CommitRegionMoveRequest{UserId: userID.String(), TargetRegion: string(target)})
	return fromGRPC("commit", err)
}

// Abort implements Identity.
func (g *GRPCIdentity) Abort(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := g.Client.AbortRegionMove(ctx, &identityv1.AbortRegionMoveRequest{UserId: userID.String(), TargetRegion: string(target)})
	return fromGRPC("abort", err)
}

// fromGRPC keeps transport failures retryable and marks the codes that no retry fixes as Permanent.
func fromGRPC(op string, err error) error {
	if err == nil {
		return nil
	}
	e := fmt.Errorf("identity %s region move: %w", op, err)
	switch status.Code(err) {
	case codes.InvalidArgument, codes.NotFound, codes.FailedPrecondition, codes.PermissionDenied, codes.Unimplemented:
		return Permanent{e}
	}
	return e
}

// LocalIdentity runs identity's half in this process.
type LocalIdentity struct{ Side *IdentitySide }

var _ Identity = (*LocalIdentity)(nil)

// Prepare implements Identity.
func (l *LocalIdentity) Prepare(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := l.Side.Prepare(ctx, userID, target)
	return err
}

// Commit implements Identity.
func (l *LocalIdentity) Commit(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := l.Side.Commit(ctx, userID, target)
	return err
}

// Abort implements Identity.
func (l *LocalIdentity) Abort(ctx context.Context, userID uuid.UUID, target region.Region) error {
	_, err := l.Side.Abort(ctx, userID, target)
	return err
}

// FromEnv builds the Coordinator when REGION_MOVE=saga and nil otherwise. The returned func releases the identity
// connection. global is the global database; regional resolves a real region's database.
func FromEnv(global DB, regional func(region.Region) (DB, error)) (*Coordinator, func(), error) {
	if !SagaEnabled() {
		return nil, func() {}, nil
	}
	c := &Coordinator{
		Store: &PGStore{DB: global},
		Org:   &OrgSide{Global: global, Regional: regional},
	}
	closeFn := func() {}
	if addr := strings.TrimSpace(os.Getenv(EnvIdentityAddr)); addr != "" {
		conn, err := rpc.Dial(addr, "identity", rpc.Options{})
		if err != nil {
			return nil, nil, fmt.Errorf("regionsaga: dial identity %s: %w", addr, err)
		}
		c.Identity = &GRPCIdentity{Client: identityv1.NewIdentityServiceClient(conn)}
		closeFn = func() { _ = conn.Close() }
	} else {
		c.Identity = &LocalIdentity{Side: &IdentitySide{Global: global, Regional: regional}}
	}
	return c, closeFn, nil
}

// Regional adapts a dbrouter-style lookup to the stores' resolver.
func Regional[P DB](lookup func(region.Region) (P, error)) func(region.Region) (DB, error) {
	return func(r region.Region) (DB, error) {
		p, err := lookup(r)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
}

// Start builds the mover for a service (nil, with a no-op stop, unless REGION_MOVE=saga) and starts the resumer
// that finishes sagas a crash left behind. stop ends the resumer and closes the identity connection.
func Start(ctx context.Context, global DB, regional func(region.Region) (DB, error), log *slog.Logger) (mover *Coordinator, stop func(), err error) {
	c, closeFn, err := FromEnv(global, regional)
	if err != nil || c == nil {
		return nil, func() {}, err
	}
	c.Log = log
	rctx, cancel := context.WithCancel(ctx)
	interval := 30 * time.Second
	if d, perr := time.ParseDuration(strings.TrimSpace(os.Getenv(ResumeIntervalEnv))); perr == nil && d > 0 {
		interval = d
	}
	go c.RunResumer(rctx, interval, time.Minute)
	return c, func() { cancel(); closeFn() }, nil
}
