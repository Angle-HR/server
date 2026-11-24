// Package kybidentity is how org talks to identity's admin module about the KYB review queue. The queue
// (admin.verification_queue) belongs to identity; org only asks it to open an item when a company needs a person
// (kyb.Queue.Enqueue). Closing an item is identity's job: it resolves the item and then calls
// AdminOrgService.ApplyKybDecision.
//
// IDENTITY_QUEUE selects the adapter:
//
//	db    (default) INTERIM: write admin.verification_queue in the global database directly (kybstore.Queue)
//	grpc  call AdminIdentityService.EnqueueVerification at IDENTITY_GRPC_ADDR
package kybidentity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment variables.
const (
	EnvQueue    = "IDENTITY_QUEUE"
	EnvGRPCAddr = "IDENTITY_GRPC_ADDR"
)

// Queue is a kyb.Queue for one region's companies.
type Queue = kyb.Queue

var (
	_ kyb.CaseQueue = (*GRPCQueue)(nil)
	_ kyb.CaseQueue = (*kybstore.Queue)(nil)
)

// GRPCQueue opens review items through identity's AdminIdentityService.
type GRPCQueue struct {
	Client identityv1.AdminIdentityServiceClient
	Region string
}

// Enqueue asks identity to open a review item. Identity treats an organization that already has an open item as
// a no-op, as the table does.
func (q *GRPCQueue) Enqueue(ctx context.Context, organizationID, countryCode string) error {
	return q.EnqueueCase(ctx, organizationID, countryCode, "")
}

// EnqueueCase is Enqueue with a case type ("individual" for a personal organization; empty means business).
func (q *GRPCQueue) EnqueueCase(ctx context.Context, organizationID, countryCode, caseType string) error {
	_, err := q.Client.EnqueueVerification(ctx, &identityv1.EnqueueVerificationRequest{
		CompanyId: organizationID, Region: q.Region, CountryCode: countryCode, CaseType: caseType,
	})
	if err != nil {
		return fmt.Errorf("kybidentity: enqueue verification: %w", err)
	}
	return nil
}

// Resolve does nothing: with the gRPC adapter identity closes the item before it calls ApplyKybDecision.
func (q *GRPCQueue) Resolve(context.Context, string, string, bool) error { return nil }

// LenientResolve wraps a queue so that resolving an item that is already closed is not an error. ApplyKybDecision
// uses it: identity (or the monolith's admin handler, in db mode) has normally closed the item first.
type LenientResolve struct{ Queue }

// Resolve implements kyb.Queue.
func (q LenientResolve) Resolve(ctx context.Context, organizationID, reviewerID string, approved bool) error {
	err := q.Queue.Resolve(ctx, organizationID, reviewerID, approved)
	if errors.Is(err, kybstore.ErrNoPendingItem) {
		return nil
	}
	return err
}

// Factory builds the queue for a region.
type Factory func(region string) Queue

// Mode reports the selected adapter: "db" (default) or "grpc".
func Mode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvQueue)), "grpc") {
		return "grpc"
	}
	return "db"
}

// NewFactory returns the Factory for the environment. global is the global database (used by the db adapter).
// The returned close func releases the gRPC connection (a no-op for db).
func NewFactory(global kybstore.DB) (Factory, func(), error) {
	if Mode() == "db" {
		return func(region string) Queue { return &kybstore.Queue{DB: global, Region: region} }, func() {}, nil
	}
	addr := strings.TrimSpace(os.Getenv(EnvGRPCAddr))
	if addr == "" {
		return nil, nil, fmt.Errorf("%s=grpc requires %s", EnvQueue, EnvGRPCAddr)
	}
	conn, err := rpc.Dial(addr, "identity", rpc.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("dial identity %s: %w", addr, err)
	}
	client := identityv1.NewAdminIdentityServiceClient(conn)
	return func(region string) Queue { return &GRPCQueue{Client: client, Region: region} }, func() { _ = conn.Close() }, nil
}
