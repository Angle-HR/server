package rpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/Angle-HR/server/pkg/platform/secrets"
)

// Service-to-service authentication, cloud neutral.
//
//	GRPC_AUTH=off    (default) no authentication; every existing stack keeps working
//	GRPC_AUTH=mtls   mutual TLS: both sides present a certificate signed by GRPC_TLS_CA; the caller is the
//	                 client certificate's common name
//	GRPC_AUTH=token  per-call HS256 token signed with the shared secret GRPC_AUTH_SECRET: audience is the
//	                 callee's service name, the issuer is the caller's, expiry is short
//
// Both modes feed the same per-method caller allow-list (AllowList). Every value comes from the environment or a
// NAME_FILE file (pkg/platform/secrets), so Docker/Kubernetes secrets and mounted Key Vault files work unchanged.

// AuthMode selects the transport authentication.
type AuthMode string

const (
	AuthOff   AuthMode = "off"
	AuthToken AuthMode = "token"
	AuthMTLS  AuthMode = "mtls"
)

const (
	// DefaultTokenTTL is how long a signed token is valid. Tokens are minted per call.
	DefaultTokenTTL = 60 * time.Second
	// clockSkew is tolerated when checking iat/exp.
	clockSkew = 10 * time.Second
	// MinSecretLen is the shortest accepted GRPC_AUTH_SECRET.
	MinSecretLen = 16

	mdAuthorization = "authorization"
)

// AuthConfig is the resolved authentication setup of one process.
type AuthConfig struct {
	Mode    AuthMode
	Service string // this process's name: the caller name on outgoing calls, the audience it accepts
	Secret  []byte // token mode
	TTL     time.Duration

	// mtls mode, PEM contents.
	CertPEM, KeyPEM, CAPEM []byte

	// Now is injectable for tests.
	Now func() time.Time
}

func (c AuthConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c AuthConfig) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return DefaultTokenTTL
}

// ServiceNameFromEnv is GRPC_SERVICE_NAME, else SERVICE_NAME, else the executable's base name.
func ServiceNameFromEnv() string {
	for _, k := range []string{"GRPC_SERVICE_NAME", "SERVICE_NAME"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	if len(os.Args) > 0 {
		return filepath.Base(os.Args[0])
	}
	return "unknown"
}

// AuthFromEnv reads GRPC_AUTH and its companions and validates them. An unknown mode or an incomplete setup is an
// error, never a silent fallback to off.
func AuthFromEnv() (AuthConfig, error) {
	cfg := AuthConfig{Mode: AuthOff, Service: ServiceNameFromEnv(), TTL: DefaultTokenTTL}
	switch m := AuthMode(strings.ToLower(strings.TrimSpace(os.Getenv("GRPC_AUTH")))); m {
	case "", AuthOff:
		return cfg, nil
	case AuthToken, AuthMTLS:
		cfg.Mode = m
	default:
		return cfg, fmt.Errorf("rpc: GRPC_AUTH=%q: want off, token or mtls", m)
	}
	switch cfg.Mode {
	case AuthToken:
		s, err := secrets.Get("GRPC_AUTH_SECRET")
		if err != nil {
			return cfg, err
		}
		if len(s) < MinSecretLen {
			return cfg, fmt.Errorf("rpc: GRPC_AUTH=token needs GRPC_AUTH_SECRET (or _FILE) of at least %d bytes", MinSecretLen)
		}
		cfg.Secret = []byte(s)
	case AuthMTLS:
		for _, p := range []struct {
			name string
			dst  *[]byte
		}{{"GRPC_TLS_CERT", &cfg.CertPEM}, {"GRPC_TLS_KEY", &cfg.KeyPEM}, {"GRPC_TLS_CA", &cfg.CAPEM}} {
			v, err := secrets.Require(p.name)
			if err != nil {
				return cfg, fmt.Errorf("rpc: GRPC_AUTH=mtls: %w", err)
			}
			*p.dst = []byte(v)
		}
	}
	if _, err := cfg.serverTLS(); cfg.Mode == AuthMTLS && err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ---- tokens -----------------------------------------------------------------------------------------------------

type tokenClaims struct {
	Iss string `json:"iss"` // caller service
	Aud string `json:"aud"` // callee service
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

var tokenHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

func sign(secret []byte, signingInput string) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(signingInput))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// MintToken returns a compact HS256 token from caller to audience.
func MintToken(secret []byte, caller, audience string, now time.Time, ttl time.Duration) string {
	b, _ := json.Marshal(tokenClaims{Iss: caller, Aud: audience, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	in := tokenHeader + "." + base64.RawURLEncoding.EncodeToString(b)
	return in + "." + sign(secret, in)
}

var errBadToken = errors.New("invalid token")

// VerifyToken checks signature, audience and expiry and returns the caller name.
func VerifyToken(secret []byte, tok, audience string, now time.Time) (string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 || parts[0] != tokenHeader {
		return "", errBadToken
	}
	want := sign(secret, parts[0]+"."+parts[1])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return "", errBadToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errBadToken
	}
	var c tokenClaims
	if json.Unmarshal(raw, &c) != nil || c.Iss == "" {
		return "", errBadToken
	}
	if c.Aud != audience {
		return "", errors.New("token audience mismatch")
	}
	if now.After(time.Unix(c.Exp, 0).Add(clockSkew)) {
		return "", errors.New("token expired")
	}
	if time.Unix(c.Iat, 0).After(now.Add(clockSkew)) {
		return "", errors.New("token issued in the future")
	}
	return c.Iss, nil
}

// ---- caller identity and allow-lists ----------------------------------------------------------------------------

type callerKey struct{}

// CallerFrom returns the authenticated calling service (empty when GRPC_AUTH=off).
func CallerFrom(ctx context.Context) string {
	s, _ := ctx.Value(callerKey{}).(string)
	return s
}

// AllowList maps a full gRPC method ("/openhr.org.v1.OrgService/GetCompany") to the services allowed to call it.
// Methods not listed accept every authenticated caller. It is enforced only when authentication is on, because
// with GRPC_AUTH=off there is no caller identity to check.
type AllowList map[string][]string

func (a AllowList) permits(method, caller string) bool {
	callers, ok := a[method]
	if !ok {
		return true
	}
	for _, c := range callers {
		if c == caller {
			return true
		}
	}
	return false
}

// ---- server side ------------------------------------------------------------------------------------------------

// serverInterceptors authenticate the caller, then apply the allow-list.
type authn struct {
	cfg   AuthConfig
	allow AllowList
}

func (a authn) identify(ctx context.Context) (string, error) {
	switch a.cfg.Mode {
	case AuthMTLS:
		p, ok := peer.FromContext(ctx)
		if !ok {
			return "", status.Error(codes.Unauthenticated, "no peer")
		}
		ti, ok := p.AuthInfo.(credentials.TLSInfo)
		if !ok || len(ti.State.VerifiedChains) == 0 || len(ti.State.VerifiedChains[0]) == 0 {
			return "", status.Error(codes.Unauthenticated, "client certificate required")
		}
		leaf := ti.State.VerifiedChains[0][0]
		if leaf.Subject.CommonName == "" {
			return "", status.Error(codes.Unauthenticated, "client certificate has no common name")
		}
		return leaf.Subject.CommonName, nil
	case AuthToken:
		md, _ := metadata.FromIncomingContext(ctx)
		v := md.Get(mdAuthorization)
		if len(v) == 0 {
			return "", status.Error(codes.Unauthenticated, "missing credentials")
		}
		tok, ok := strings.CutPrefix(v[0], "Bearer ")
		if !ok {
			return "", status.Error(codes.Unauthenticated, "malformed credentials")
		}
		caller, err := VerifyToken(a.cfg.Secret, tok, a.cfg.Service, a.cfg.now())
		if err != nil {
			return "", status.Error(codes.Unauthenticated, err.Error())
		}
		return caller, nil
	}
	return "", nil
}

func (a authn) check(ctx context.Context, method string) (context.Context, error) {
	caller, err := a.identify(ctx)
	if err != nil {
		return ctx, err
	}
	if !a.allow.permits(method, caller) {
		return ctx, status.Errorf(codes.PermissionDenied, "service %q may not call %s", caller, method)
	}
	return context.WithValue(ctx, callerKey{}, caller), nil
}

// ServerSecurity returns the grpc.ServerOptions that enforce cfg: TLS credentials in mtls mode, authentication
// and allow-list interceptors in both, and a keepalive enforcement policy matching the clients. With Mode off it
// returns only the keepalive policy.
func ServerSecurity(cfg AuthConfig, allow AllowList) ([]grpc.ServerOption, error) {
	opts := []grpc.ServerOption{grpc.KeepaliveEnforcementPolicy(serverKeepalivePolicy)}
	if cfg.Mode == AuthOff || cfg.Mode == "" {
		return opts, nil
	}
	if cfg.Service == "" {
		return nil, errors.New("rpc: service name is empty")
	}
	if cfg.Mode == AuthMTLS {
		tc, err := cfg.serverTLS()
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tc)))
	}
	a := authn{cfg: cfg, allow: allow}
	opts = append(opts,
		grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			ctx, err := a.check(ctx, info.FullMethod)
			if err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
			ctx, err := a.check(ss.Context(), info.FullMethod)
			if err != nil {
				return err
			}
			return h(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
		}))
	return opts, nil
}

// ServerOptions is ServerSecurity for the process environment. defaultService names the service when neither
// GRPC_SERVICE_NAME nor SERVICE_NAME is set. A bad configuration panics at start-up: a service that was asked to
// authenticate must never come up open.
func ServerOptions(defaultService string, allow AllowList) []grpc.ServerOption {
	cfg, err := AuthFromEnv()
	if err != nil {
		panic(err)
	}
	if os.Getenv("GRPC_SERVICE_NAME") == "" && os.Getenv("SERVICE_NAME") == "" && defaultService != "" {
		cfg.Service = defaultService
	}
	opts, err := ServerSecurity(cfg, allow)
	if err != nil {
		panic(err)
	}
	return opts
}

func (c AuthConfig) serverTLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("rpc: GRPC_TLS_CERT/KEY: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CAPEM) {
		return nil, errors.New("rpc: GRPC_TLS_CA holds no certificate")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func (c AuthConfig) clientTLS() (*tls.Config, error) {
	cert, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("rpc: GRPC_TLS_CERT/KEY: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(c.CAPEM) {
		return nil, errors.New("rpc: GRPC_TLS_CA holds no certificate")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool, MinVersion: tls.VersionTLS13}, nil
}

// ---- client side ------------------------------------------------------------------------------------------------

type tokenCreds struct {
	cfg      AuthConfig
	audience string
}

func (t tokenCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{mdAuthorization: "Bearer " + MintToken(t.cfg.Secret, t.cfg.Service, t.audience, t.cfg.now(), t.cfg.ttl())}, nil
}

// Tokens are accepted over a plaintext link too: that is the point of token mode on a trusted network.
func (tokenCreds) RequireTransportSecurity() bool { return false }

// ClientSecurity returns the dial options that authenticate calls to the service named audience: client
// certificate and CA in mtls mode, a per-call token in token mode, plaintext otherwise.
func ClientSecurity(cfg AuthConfig, audience string) ([]grpc.DialOption, error) {
	switch cfg.Mode {
	case AuthMTLS:
		tc, err := cfg.clientTLS()
		if err != nil {
			return nil, err
		}
		return []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tc))}, nil
	case AuthToken:
		if audience == "" {
			return nil, errors.New("rpc: token mode needs the audience (callee service name)")
		}
		return []grpc.DialOption{
			grpc.WithTransportCredentials(insecureCreds()),
			grpc.WithPerRPCCredentials(tokenCreds{cfg: cfg, audience: audience}),
		}, nil
	}
	return []grpc.DialOption{grpc.WithTransportCredentials(insecureCreds())}, nil
}
