package applicantcounts

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	applicationsv1 "github.com/Angle-HR/server/gen/openhr/applications/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/pkg/rpc"
)

type stub struct {
	applicationsv1.UnimplementedApplicationsServiceServer
	gotCompany, gotRegion string
	gotIDs                []string
}

func (s *stub) GetApplicantCounts(ctx context.Context, r *applicationsv1.GetApplicantCountsRequest) (*applicationsv1.GetApplicantCountsResponse, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	s.gotCompany, s.gotRegion, s.gotIDs = cc.GetCompanyId(), cc.GetRegion(), r.GetJobIds()
	return &applicationsv1.GetApplicantCountsResponse{Counts: []*applicationsv1.ApplicantCount{
		{JobId: "j1", Total: 7, NewCount: 3}, {JobId: "j2"},
	}}, nil
}

func TestGRPCSendsCallContextAndBatches(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	st := &stub{}
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(rpc.ServerUnaryInterceptor(rpc.Options{})))
	applicationsv1.RegisterApplicationsServiceServer(srv, st)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(rpc.ClientUnaryInterceptor(rpc.Options{})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	g := GRPC{Client: applicationsv1.NewApplicationsServiceClient(conn)}
	got, err := g.ApplicantCounts(context.Background(), draft.Caller{UserID: "u", OrgID: "c1", Region: "uk"}, []string{"j1", "j2", "j3"})
	if err != nil {
		t.Fatal(err)
	}
	if st.gotCompany != "c1" || st.gotRegion != "uk" || len(st.gotIDs) != 3 {
		t.Fatalf("call context or batch wrong: %+v", st)
	}
	if got["j1"] != (draft.ApplicantCount{Total: 7, New: 3}) || got["j2"] != (draft.ApplicantCount{}) {
		t.Fatalf("got %+v", got)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvMode, "grpc")
	t.Setenv(EnvAddr, "")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("grpc without an address must fail")
	}
	t.Setenv(EnvMode, "bogus")
	if _, _, err := FromEnv(nil); err == nil {
		t.Fatal("unknown mode must fail")
	}
	t.Setenv(EnvMode, "")
	c, _, err := FromEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.(DB); !ok {
		t.Fatalf("default must be db, got %T", c)
	}
}
