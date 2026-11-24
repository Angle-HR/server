package rpc

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	_ "github.com/Angle-HR/server/gen/openhr/applications/v1"
	_ "github.com/Angle-HR/server/gen/openhr/hiring/v1"
	_ "github.com/Angle-HR/server/gen/openhr/identity/v1"
	_ "github.com/Angle-HR/server/gen/openhr/notifications/v1"
	_ "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/pkg/rpc/devca"
)

const secret = "0123456789abcdef-test-secret"
const checkMethod = "/grpc.health.v1.Health/Check"

// start serves the standard health service with cfg's security and returns its address.
func start(t *testing.T, cfg AuthConfig, allow AllowList) string {
	t.Helper()
	opts, err := ServerSecurity(cfg, allow)
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer(opts...)
	healthpb.RegisterHealthServer(s, health.NewServer())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	_, port, _ := net.SplitHostPort(lis.Addr().String())
	return "localhost:" + port
}

func call(t *testing.T, cfg AuthConfig, addr, audience string) error {
	t.Helper()
	conn, err := DialWith(cfg, addr, audience, Options{DefaultTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	return err
}

func code(err error) codes.Code { return status.Code(err) }

func tokenCfg(service string) AuthConfig {
	return AuthConfig{Mode: AuthToken, Service: service, Secret: []byte(secret)}
}

func TestTokenAccepted(t *testing.T) {
	addr := start(t, tokenCfg("org"), nil)
	if err := call(t, tokenCfg("hiring"), addr, "org"); err != nil {
		t.Fatal(err)
	}
}

func TestTokenUnauthenticatedRejected(t *testing.T) {
	addr := start(t, tokenCfg("org"), nil)
	if err := call(t, AuthConfig{Mode: AuthOff}, addr, "org"); code(err) != codes.Unauthenticated {
		t.Fatalf("got %v, want Unauthenticated", err)
	}
}

func TestTokenWrongAudience(t *testing.T) {
	addr := start(t, tokenCfg("org"), nil)
	err := call(t, tokenCfg("hiring"), addr, "identity")
	if code(err) != codes.Unauthenticated || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("got %v, want audience Unauthenticated", err)
	}
}

func TestTokenExpired(t *testing.T) {
	addr := start(t, tokenCfg("org"), nil)
	c := tokenCfg("hiring")
	c.Now = func() time.Time { return time.Now().Add(-time.Hour) }
	err := call(t, c, addr, "org")
	if code(err) != codes.Unauthenticated || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("got %v, want expired Unauthenticated", err)
	}
}

func TestTokenWrongSecret(t *testing.T) {
	addr := start(t, tokenCfg("org"), nil)
	c := tokenCfg("hiring")
	c.Secret = []byte("a-different-secret-entirely")
	if err := call(t, c, addr, "org"); code(err) != codes.Unauthenticated {
		t.Fatalf("got %v", err)
	}
}

func TestAllowList(t *testing.T) {
	addr := start(t, tokenCfg("org"), AllowList{checkMethod: {"hiring"}})
	if err := call(t, tokenCfg("hiring"), addr, "org"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, tokenCfg("notifications"), addr, "org"); code(err) != codes.PermissionDenied {
		t.Fatalf("got %v, want PermissionDenied", err)
	}
}

func TestVerifyTokenGarbage(t *testing.T) {
	for _, tok := range []string{"", "a.b.c", "x", tokenHeader + ".e30.AAAA"} {
		if _, err := VerifyToken([]byte(secret), tok, "org", time.Now()); err == nil {
			t.Errorf("%q accepted", tok)
		}
	}
}

func mtlsCfg(t *testing.T, ca *devca.CA, cn string) AuthConfig {
	t.Helper()
	crt, key, err := ca.Issue(cn, []string{cn, "localhost", "127.0.0.1"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return AuthConfig{Mode: AuthMTLS, Service: cn, CertPEM: crt, KeyPEM: key, CAPEM: ca.PEM}
}

func newCA(t *testing.T) *devca.CA {
	ca, err := devca.NewCA("test-ca", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestMTLSAcceptedAndCallerIsCN(t *testing.T) {
	ca := newCA(t)
	// The allow-list proves the certificate common name is the caller identity.
	addr := start(t, mtlsCfg(t, ca, "org"), AllowList{checkMethod: {"hiring"}})
	if err := call(t, mtlsCfg(t, ca, "hiring"), addr, "org"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, mtlsCfg(t, ca, "identity"), addr, "org"); code(err) != codes.PermissionDenied {
		t.Fatalf("got %v, want PermissionDenied", err)
	}
}

func TestMTLSPlaintextRejected(t *testing.T) {
	ca := newCA(t)
	addr := start(t, mtlsCfg(t, ca, "org"), nil)
	if err := call(t, AuthConfig{Mode: AuthOff}, addr, "org"); err == nil {
		t.Fatal("plaintext client reached an mtls server")
	}
}

func TestMTLSTokenOnlyClientRejected(t *testing.T) {
	ca := newCA(t)
	addr := start(t, mtlsCfg(t, ca, "org"), nil)
	if err := call(t, tokenCfg("hiring"), addr, "org"); err == nil {
		t.Fatal("token client reached an mtls server")
	}
}

func TestMTLSWrongCA(t *testing.T) {
	good, other := newCA(t), newCA(t)
	addr := start(t, mtlsCfg(t, good, "org"), nil)
	// Client certificate from another CA, also trusting the other CA: both directions fail.
	if err := call(t, mtlsCfg(t, other, "hiring"), addr, "org"); err == nil {
		t.Fatal("client from a different CA was accepted")
	}
	// Right client certificate but the server's certificate is not trusted.
	c := mtlsCfg(t, good, "hiring")
	c.CAPEM = other.PEM
	if err := call(t, c, addr, "org"); err == nil {
		t.Fatal("server certificate from an untrusted CA was accepted")
	}
}

func TestAuthFromEnv(t *testing.T) {
	t.Setenv("GRPC_AUTH_SECRET", "")
	t.Setenv("GRPC_TLS_CERT", "")
	t.Setenv("GRPC_TLS_KEY", "")
	t.Setenv("GRPC_TLS_CA", "")
	t.Setenv("GRPC_AUTH", "")
	if c, err := AuthFromEnv(); err != nil || c.Mode != AuthOff {
		t.Fatalf("default: %v %v", c.Mode, err)
	}
	t.Setenv("GRPC_AUTH", "bogus")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("unknown mode accepted")
	}
	t.Setenv("GRPC_AUTH", "token")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("token without secret accepted")
	}
	t.Setenv("GRPC_AUTH_SECRET", "short")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("short secret accepted")
	}
	t.Setenv("GRPC_AUTH_SECRET", secret)
	t.Setenv("GRPC_SERVICE_NAME", "hiring")
	c, err := AuthFromEnv()
	if err != nil || c.Service != "hiring" || c.Mode != AuthToken {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("GRPC_AUTH", "mtls")
	if _, err := AuthFromEnv(); err == nil {
		t.Fatal("mtls without certificates accepted")
	}
}

func TestServiceConfigRetriesOnlySafeMethods(t *testing.T) {
	// The generated services are registered by the packages the binaries import; import-free check on names.
	cfg := ServiceConfig()
	for _, good := range []string{"ResolveMembership", "GetPublishReadiness", "GetApplicantCounts", "ListJobs"} {
		if !strings.Contains(cfg, `"`+good+`"`) {
			t.Errorf("safe %s is not retried", good)
		}
	}
	for _, bad := range []string{"SubmitApplication", "PublishJob", "ApplyKybDecision", "AcceptAgreement", "SendEmail"} {
		if strings.Contains(cfg, `"`+bad+`"`) {
			t.Errorf("non-idempotent %s is retried", bad)
		}
	}
}

func TestTarget(t *testing.T) {
	for in, want := range map[string]string{
		"org:9090": "dns:///org:9090", "dns:///x:1": "dns:///x:1", "passthrough:///bufnet": "passthrough:///bufnet",
		"unix:/tmp/s": "unix:/tmp/s",
	} {
		if got := Target(in); got != want {
			t.Errorf("Target(%q)=%q want %q", in, got, want)
		}
	}
}
