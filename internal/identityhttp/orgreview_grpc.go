package identityhttp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc/codes"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rpc"
	"google.golang.org/grpc/status"
)

// Environment variables that select the OrgReview implementation.
const (
	// EnvOrgApply is "db" (default, interim: write org's tables directly) or "grpc" (AdminOrgService.ApplyKybDecision).
	EnvOrgApply = "ORG_APPLY"
	// EnvOrgGRPCAddr is the org service's gRPC address for ORG_APPLY=grpc, for example org:9090.
	EnvOrgGRPCAddr = "ORG_GRPC_ADDR"
)

// OrgGRPC applies decisions through org's AdminOrgService.
type OrgGRPC struct {
	Client orgv1.AdminOrgServiceClient
	// Details reads a case's submitted details. The contract has no RPC for them (registration number, address),
	// so identity falls back to its read of org's regional tables (INTERIM, only this read stays on the database).
	// Nil: the detail route answers 503 (ErrDetailUnavailable); the decision itself works either way.
	Details interface {
		Detail(ctx context.Context, companyID string, reg region.Region) (CaseDetail, error)
	}
}

// Detail implements OrgReview.
func (o *OrgGRPC) Detail(ctx context.Context, companyID string, reg region.Region) (CaseDetail, error) {
	if o.Details == nil {
		return CaseDetail{}, ErrDetailUnavailable
	}
	return o.Details.Detail(ctx, companyID, reg)
}

// Apply implements OrgReview.
func (o *OrgGRPC) Apply(ctx context.Context, d Decision) (Outcome, error) {
	resp, err := o.Client.ApplyKybDecision(ctx, &orgv1.ApplyKybDecisionRequest{
		CompanyId: d.CompanyID, Region: string(d.Region), ReviewerAdminId: d.ReviewerID, Approve: d.Approve, Reason: d.Reason,
	})
	if err != nil {
		return Outcome{}, grpcToAppError(err)
	}
	out := Outcome{Status: "failed", FailureReason: d.Reason}
	if resp.GetStatus() == orgv1.KybStatus_KYB_STATUS_VERIFIED {
		out = Outcome{Status: "verified"}
	}
	return out, nil
}

func grpcToAppError(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	case codes.NotFound:
		return apperror.ErrNotFound
	case codes.FailedPrecondition, codes.AlreadyExists:
		return apperror.New(apperror.CodeConflict, "action not available for the current verification status")
	case codes.Unavailable, codes.DeadlineExceeded:
		return errUnavailable
	default:
		return err
	}
}

// OrgReviewFromEnv builds the OrgReview selected by ORG_APPLY. The returned func releases the connection.
func OrgReviewFromEnv(router *dbrouter.DBRouter, notifierFor KYBNotifierFactory) (OrgReview, func(), error) {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvOrgApply))); mode {
	case "", "db":
		return &OrgDB{Router: router, NotifierFor: notifierFor}, func() {}, nil
	case "grpc":
		addr := strings.TrimSpace(os.Getenv(EnvOrgGRPCAddr))
		if addr == "" {
			return nil, nil, fmt.Errorf("%s=grpc requires %s", EnvOrgApply, EnvOrgGRPCAddr)
		}
		conn, err := rpc.Dial(addr, "org", rpc.Options{})
		if err != nil {
			return nil, nil, fmt.Errorf("org grpc client: %w", err)
		}
		return &OrgGRPC{
			Client: orgv1.NewAdminOrgServiceClient(conn), Details: &OrgDB{Router: router, NotifierFor: notifierFor},
		}, func() { _ = conn.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("%s=%q: want db or grpc", EnvOrgApply, mode)
	}
}
