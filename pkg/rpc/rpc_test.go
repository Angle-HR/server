package rpc_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	commonv1 "github.com/Angle-HR/server/gen/openhr/common/v1"
	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	"github.com/Angle-HR/server/pkg/rpc"
)

// seen records what the handler observed, so the test can assert on propagation.
type seen struct {
	cc       *commonv1.CallContext
	deadline bool
}

type identityServer struct {
	identityv1.UnimplementedIdentityServiceServer
	got *seen
}

func (s *identityServer) GetUser(ctx context.Context, req *identityv1.GetUserRequest) (*identityv1.GetUserResponse, error) {
	cc, _ := rpc.CallContextFrom(ctx)
	_, s.got.deadline = ctx.Deadline()
	s.got.cc = cc
	return &identityv1.GetUserResponse{User: &identityv1.User{Id: req.UserId, Region: cc.Region}}, nil
}

func dial(t *testing.T, o rpc.Options) (identityv1.IdentityServiceClient, *seen) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	got := &seen{}
	srv := grpc.NewServer(grpc.UnaryInterceptor(rpc.ServerUnaryInterceptor(o)))
	identityv1.RegisterIdentityServiceServer(srv, &identityServer{got: got})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(rpc.ClientUnaryInterceptor(o)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return identityv1.NewIdentityServiceClient(conn), got
}

func TestCallContextPropagates(t *testing.T) {
	client, got := dial(t, rpc.Options{})
	want := &commonv1.CallContext{
		Region: "uk", CompanyId: "c-1", UserId: "u-1",
		Permissions: []string{"job.create", "job.view_all"},
		TraceId:     "4bf92f3577b34da6a3ce929d0e0e4736",
		RequestId:   "req-1",
		ActorType:   commonv1.ActorType_ACTOR_TYPE_USER,
	}
	ctx := rpc.WithCallContext(context.Background(), want)

	resp, err := client.GetUser(ctx, &identityv1.GetUserRequest{UserId: "u-1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.User.Id != "u-1" || resp.User.Region != "uk" {
		t.Fatalf("response = %+v", resp.User)
	}
	g := got.cc
	if g.Region != "uk" || g.CompanyId != "c-1" || g.UserId != "u-1" || g.RequestId != "req-1" ||
		g.TraceId != want.TraceId || g.ActorType != want.ActorType {
		t.Fatalf("call context lost in transit: %+v", g)
	}
	if len(g.Permissions) != 2 || !rpc.HasPermission(g, "job.view_all") || rpc.HasPermission(g, "job.export") {
		t.Fatalf("permissions = %v", g.Permissions)
	}
}

func TestDefaultsAreGenerated(t *testing.T) {
	client, got := dial(t, rpc.Options{DefaultTimeout: 2 * time.Second})
	if _, err := client.GetUser(context.Background(), &identityv1.GetUserRequest{UserId: "u-2"}); err != nil {
		t.Fatal(err)
	}
	if len(got.cc.TraceId) != 32 || got.cc.RequestId == "" {
		t.Fatalf("ids not generated: %+v", got.cc)
	}
	if !got.deadline {
		t.Fatal("default deadline was not applied")
	}
}

func TestCustomTraceIDHook(t *testing.T) {
	const tid = "0af7651916cd43dd8448eb211c80319c"
	client, got := dial(t, rpc.Options{TraceID: func(context.Context) string { return tid }})
	if _, err := client.GetUser(context.Background(), &identityv1.GetUserRequest{UserId: "u-3"}); err != nil {
		t.Fatal(err)
	}
	if got.cc.TraceId != tid {
		t.Fatalf("trace id = %q, want %q", got.cc.TraceId, tid)
	}
}

func TestTraceparent(t *testing.T) {
	h := rpc.FormatTraceparent("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7")
	if h != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
		t.Fatal(h)
	}
	if id, ok := rpc.ParseTraceparent(h); !ok || id != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatal("parse failed")
	}
	for _, bad := range []string{"", "00-xyz-1-01", "00-00000000000000000000000000000000-00f067aa0ba902b7-01"} {
		if _, ok := rpc.ParseTraceparent(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
}
