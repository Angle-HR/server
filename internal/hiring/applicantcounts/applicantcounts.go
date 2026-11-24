// Package applicantcounts supplies the applicant counters the hiring job list shows. Two implementations of
// draft.ApplicantCounts, selected by APPLICANT_COUNTS:
//
//	db    (default) reads hiring.job_applicant_counts of the caller's region, as hiring always did.
//	grpc  asks ApplicationsService.GetApplicantCounts (one batched call per list page) at APPLICATIONS_GRPC_ADDR.
package applicantcounts

import (
	"context"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment variables.
const (
	EnvMode = "APPLICANT_COUNTS"
	EnvAddr = "APPLICATIONS_GRPC_ADDR"

	ModeDB   = "db"
	ModeGRPC = "grpc"
)

// DB reads the counters from the regional database.
type DB struct {
	// Regional returns the store of a region.
	Regional func(region string) (*hiringstore.Store, error)
}

// ApplicantCounts implements draft.ApplicantCounts.
func (d DB) ApplicantCounts(ctx context.Context, c draft.Caller, jobIDs []string) (map[string]draft.ApplicantCount, error) {
	st, err := d.Regional(c.Region)
	if err != nil {
		return nil, err
	}
	rows, err := st.ApplicantCounts(ctx, c.OrgID, jobIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]draft.ApplicantCount, len(rows))
	for _, r := range rows {
		out[r.JobID] = draft.ApplicantCount{Total: r.Total, New: r.New}
	}
	return out, nil
}

// GRPC asks the applications service.
type GRPC struct {
	Client applicationsv1.ApplicationsServiceClient
}

// ApplicantCounts implements draft.ApplicantCounts. The caller's company and region travel as CallContext
// metadata; the client interceptor of pkg/rpc forwards them.
func (g GRPC) ApplicantCounts(ctx context.Context, c draft.Caller, jobIDs []string) (map[string]draft.ApplicantCount, error) {
	ctx = rpc.WithCallContext(ctx, &commonv1.CallContext{UserId: c.UserID, CompanyId: c.OrgID, Region: c.Region})
	resp, err := g.Client.GetApplicantCounts(ctx, &applicationsv1.GetApplicantCountsRequest{JobIds: jobIDs})
	if err != nil {
		return nil, fmt.Errorf("applicantcounts: %w", err)
	}
	out := make(map[string]draft.ApplicantCount, len(resp.GetCounts()))
	for _, n := range resp.GetCounts() {
		out[n.GetJobId()] = draft.ApplicantCount{Total: int(n.GetTotal()), New: int(n.GetNewCount())}
	}
	return out, nil
}

// NewGRPC dials addr (plaintext: service to service inside the cluster network) and returns the implementation and
// the connection to close.
func NewGRPC(addr string) (*GRPC, *grpc.ClientConn, error) {
	conn, err := rpc.Dial(addr, "applications", rpc.Options{})
	if err != nil {
		return nil, nil, fmt.Errorf("applicantcounts: dial %s: %w", addr, err)
	}
	return &GRPC{Client: applicationsv1.NewApplicationsServiceClient(conn)}, conn, nil
}

// FromEnv picks the implementation. regional serves the db mode. The returned close func is never nil.
func FromEnv(regional func(region string) (*hiringstore.Store, error)) (draft.ApplicantCounts, func(), error) {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv(EnvMode))); mode {
	case "", ModeDB:
		return DB{Regional: regional}, func() {}, nil
	case ModeGRPC:
		addr := strings.TrimSpace(os.Getenv(EnvAddr))
		if addr == "" {
			return nil, nil, fmt.Errorf("%s=grpc requires %s", EnvMode, EnvAddr)
		}
		g, conn, err := NewGRPC(addr)
		if err != nil {
			return nil, nil, err
		}
		return g, func() { _ = conn.Close() }, nil
	default:
		return nil, nil, fmt.Errorf("%s: unknown value %q (want db or grpc)", EnvMode, mode)
	}
}
