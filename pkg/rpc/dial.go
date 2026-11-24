package rpc

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/resolver/dns"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

func insecureCreds() credentials.TransportCredentials { return insecure.NewCredentials() }

// Resilience defaults shared by every client.
const (
	// ResolveInterval bounds how often the DNS resolver may be asked again after a connection failure. grpc-go's
	// default is 30s, which is how a recreated peer (new container IP) stayed unreachable for half a minute.
	ResolveInterval = 2 * time.Second
	// RetryAttempts is the retry budget for safe methods (the first try included).
	RetryAttempts = 4
)

var serverKeepalivePolicy = keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}

func init() { dns.SetMinResolutionInterval(ResolveInterval) }

// Target turns "host:port" into "dns:///host:port" so the DNS resolver, and with it re-resolution after the peer
// moved, is always in use. Targets that already carry a scheme (dns:, passthrough:, unix:, bufnet) are untouched.
func Target(addr string) string {
	if strings.Contains(addr, "://") || strings.HasPrefix(addr, "unix:") || strings.HasPrefix(addr, "dns:") {
		return addr
	}
	return "dns:///" + addr
}

// safeMethodPrefixes name read-only RPCs by convention (Get*, List*, Resolve*, Validate*, Lookup*, Check*, Has*,
// BatchGet*). Only these get retry and wait-for-ready; everything else (Submit, Create, Publish, Accept, Apply,
// ChangeStatus, ...) fails fast and is never retried, unless the proto marks it idempotent.
var safeMethodPrefixes = []string{"Get", "List", "BatchGet", "Resolve", "Validate", "Lookup", "Check", "Has"}

// SafeMethods returns the "package.Service"/"Method" pairs of every registered RPC that is read-only by name or
// marked idempotency_level NO_SIDE_EFFECTS or IDEMPOTENT.
func SafeMethods() [][2]string {
	var out [][2]string
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		svcs := fd.Services()
		for i := 0; i < svcs.Len(); i++ {
			s := svcs.Get(i)
			ms := s.Methods()
			for j := 0; j < ms.Len(); j++ {
				m := ms.Get(j)
				if isSafe(m) {
					out = append(out, [2]string{string(s.FullName()), string(m.Name())})
				}
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

func isSafe(m protoreflect.MethodDescriptor) bool {
	if o, ok := m.Options().(*descriptorpb.MethodOptions); ok && o != nil {
		switch o.GetIdempotencyLevel() {
		case descriptorpb.MethodOptions_NO_SIDE_EFFECTS, descriptorpb.MethodOptions_IDEMPOTENT:
			return true
		}
	}
	name := string(m.Name())
	for _, p := range safeMethodPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ServiceConfig is the gRPC service config JSON that retries only the safe methods, on UNAVAILABLE, with
// exponential backoff, and makes them wait for the channel to become ready instead of failing on a stale
// address. Calls remain bounded by the default deadline (ClientUnaryInterceptor).
func ServiceConfig() string {
	var names []map[string]string
	for _, sm := range SafeMethods() {
		names = append(names, map[string]string{"service": sm[0], "method": sm[1]})
	}
	cfg := map[string]any{}
	if len(names) > 0 {
		cfg["methodConfig"] = []any{map[string]any{
			"name":         names,
			"waitForReady": true,
			"retryPolicy": map[string]any{
				"maxAttempts":          RetryAttempts,
				"initialBackoff":       "0.2s",
				"maxBackoff":           "2s",
				"backoffMultiplier":    2.0,
				"retryableStatusCodes": []string{"UNAVAILABLE"},
			},
		}}
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

// Dial opens a lazy client connection to the service named audience at addr: DNS resolution with periodic
// re-resolution, reconnect backoff capped at 3s, keepalive, retries for safe methods, the CallContext and default
// deadline interceptors, and the authentication selected by GRPC_AUTH. extra options are applied last.
func Dial(addr, audience string, o Options, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	cfg, err := AuthFromEnv()
	if err != nil {
		return nil, err
	}
	return DialWith(cfg, addr, audience, o, extra...)
}

// DialWith is Dial with an explicit AuthConfig (tests).
func DialWith(cfg AuthConfig, addr, audience string, o Options, extra ...grpc.DialOption) (*grpc.ClientConn, error) {
	sec, err := ClientSecurity(cfg, audience)
	if err != nil {
		return nil, err
	}
	opts := append(sec,
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: backoff.Config{
			BaseDelay: 200 * time.Millisecond, Multiplier: 1.6, Jitter: 0.2, MaxDelay: 3 * time.Second,
		}, MinConnectTimeout: 3 * time.Second}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
		grpc.WithDefaultServiceConfig(ServiceConfig()),
		grpc.WithChainUnaryInterceptor(ClientUnaryInterceptor(o)),
		grpc.WithChainStreamInterceptor(ClientStreamInterceptor(o)),
	)
	opts = append(opts, extra...)
	conn, err := grpc.NewClient(Target(addr), opts...)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial %s: %w", addr, err)
	}
	return conn, nil
}
