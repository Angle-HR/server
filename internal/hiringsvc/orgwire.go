package hiringsvc

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	identityv1 "github.com/Angle-HR/server/gen/openhr/identity/v1"
	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringhttp"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/orgclient"
	"github.com/Angle-HR/server/pkg/rpc"
)

// Environment selecting how hiring reads what org owns. Every switch defaults to "db": the interim adapters over
// the shared accounts.* tables, i.e. today's behaviour.
const (
	// EnvOrgReadiness: db | grpc. grpc asks OrgService.GetPublishReadiness before every publish and fails closed.
	EnvOrgReadiness = "ORG_READINESS"
	// EnvOrgMembership: db | grpc. grpc resolves the caller's company and permissions with
	// OrgService.ResolveMembership on every request.
	EnvOrgMembership = "ORG_MEMBERSHIP"
	// EnvOrgIndustries: db | grpc. grpc validates and lists job industries through OrgService.ListIndustries
	// (cached, degrading to the database).
	EnvOrgIndustries = "ORG_INDUSTRIES"
	// EnvOrgCompany: db | grpc. grpc reads the company row of the public page and the job events through
	// OrgService.GetCompany (cached, stale on error), the settings page through GetPublishReadiness, and accepts
	// the DPA and sets the privacy contact through AcceptAgreement and SetPrivacyContact.
	EnvOrgCompany = "ORG_COMPANY"
	// EnvIdentityUsers: db | grpc. grpc resolves the hiring managers' names on the job list through
	// IdentityService.BatchGetUsers at IDENTITY_GRPC_ADDR.
	EnvIdentityUsers = "IDENTITY_USERS"
	// EnvIdentityGRPCAddr is the identity service's gRPC address.
	EnvIdentityGRPCAddr = "IDENTITY_GRPC_ADDR"
	// EnvOrgGRPCAddr is the org service's gRPC address, for example org:9090. Required when any switch is grpc.
	EnvOrgGRPCAddr = "ORG_GRPC_ADDR"
	// EnvOrgGRPCTimeout bounds each org call (Go duration, default 3s).
	EnvOrgGRPCTimeout = "ORG_GRPC_TIMEOUT"
)

// OrgMode is one switch's value.
type OrgMode string

// Modes.
const (
	ModeDB   OrgMode = "db"
	ModeGRPC OrgMode = "grpc"
)

// ParseMode reads a db|grpc switch; empty means db.
func ParseMode(name, v string) (OrgMode, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "db":
		return ModeDB, nil
	case "grpc":
		return ModeGRPC, nil
	}
	return "", fmt.Errorf("%s=%q: want db or grpc", name, v)
}

// OrgWiring is the selected org adapters, ready to plug into the directory.
type OrgWiring struct {
	Readiness  orgclient.ReadinessSource
	Membership orgclient.MembershipSource
	// Industries wraps the reference data when ORG_INDUSTRIES=grpc; nil otherwise.
	Industries func(base draft.Reference) draft.Reference
	// Company and Users are set when ORG_COMPANY / IDENTITY_USERS are grpc; nil otherwise.
	Company hiringstore.CompanyOrg
	Users   hiringstore.UserNames
	Close   func()
	Modes   map[string]OrgMode
}

// Apply sets the selected adapters on the directory.
func (w *OrgWiring) Apply(d *hiringhttp.PostgresHiringDirectory, global draft.Reference) {
	d.Readiness, d.Members = w.Readiness, w.Membership
	d.Company, d.Users = w.Company, w.Users
	if w.Industries != nil {
		d.Reference = w.Industries(global)
	}
}

// NewOrgWiring reads the ORG_* environment (getenv is os.Getenv in production) and dials org once if any switch
// asks for it. The connection is lazy: an unreachable org shows up as failed calls, never as a failed start, so
// the service comes up and drafting works while org is down.
func NewOrgWiring(getenv func(string) string, log *slog.Logger) (*OrgWiring, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	w := &OrgWiring{Close: func() {}, Modes: map[string]OrgMode{}}
	var modes [3]OrgMode
	for i, name := range []string{EnvOrgReadiness, EnvOrgMembership, EnvOrgIndustries} {
		m, err := ParseMode(name, getenv(name))
		if err != nil {
			return nil, err
		}
		modes[i], w.Modes[name] = m, m
	}
	company, err := ParseMode(EnvOrgCompany, getenv(EnvOrgCompany))
	if err != nil {
		return nil, err
	}
	users, err := ParseMode(EnvIdentityUsers, getenv(EnvIdentityUsers))
	if err != nil {
		return nil, err
	}
	w.Modes[EnvOrgCompany], w.Modes[EnvIdentityUsers] = company, users
	timeout := orgclient.DefaultTimeout
	if v := strings.TrimSpace(getenv(EnvOrgGRPCTimeout)); v != "" {
		t, terr := time.ParseDuration(v)
		if terr != nil || t <= 0 {
			return nil, fmt.Errorf("%s=%q: want a positive duration such as 3s", EnvOrgGRPCTimeout, v)
		}
		timeout = t
	}
	if users == ModeGRPC {
		idAddr := strings.TrimSpace(getenv(EnvIdentityGRPCAddr))
		if idAddr == "" {
			return nil, fmt.Errorf("%s is required when %s is grpc", EnvIdentityGRPCAddr, EnvIdentityUsers)
		}
		idConn, derr := Dial(idAddr, timeout)
		if derr != nil {
			return nil, derr
		}
		prev := w.Close
		w.Close = func() { prev(); _ = idConn.Close() }
		w.Users = &orgclient.GRPCUsers{Client: identityv1.NewIdentityServiceClient(idConn), Timeout: timeout}
		log.Info("identity client", "addr", idAddr, "users", "grpc")
	}
	if modes[0] != ModeGRPC && modes[1] != ModeGRPC && modes[2] != ModeGRPC && company != ModeGRPC {
		return w, nil
	}
	addr := strings.TrimSpace(getenv(EnvOrgGRPCAddr))
	if addr == "" {
		return nil, fmt.Errorf("%s is required when an ORG_* switch is grpc", EnvOrgGRPCAddr)
	}
	conn, err := Dial(addr, timeout)
	if err != nil {
		return nil, err
	}
	prevClose := w.Close
	w.Close = func() { prevClose(); _ = conn.Close() }
	client := orgv1.NewOrgServiceClient(conn)
	if modes[0] == ModeGRPC {
		w.Readiness = &orgclient.GRPCReadiness{Client: client, Timeout: timeout}
	}
	if modes[1] == ModeGRPC {
		w.Membership = &orgclient.GRPCMembership{Client: client, Timeout: timeout}
	}
	if company == ModeGRPC {
		w.Company = &orgclient.GRPCCompany{Client: client, Timeout: timeout}
	}
	if modes[2] == ModeGRPC {
		w.Industries = func(base draft.Reference) draft.Reference {
			return &orgclient.Industries{Reference: base, Client: client, Timeout: timeout}
		}
	}
	log.Info("org client", "addr", addr, "timeout", timeout.String(), "readiness", string(modes[0]),
		"membership", string(modes[1]), "industries", string(modes[2]), "company", string(company))
	return w, nil
}

// Dial opens a lazy connection to the org service (DNS re-resolution, retries for safe calls, GRPC_AUTH
// authentication; see rpc.Dial) that forwards the CallContext and bounds calls without a deadline.
func Dial(addr string, timeout time.Duration) (*grpc.ClientConn, error) {
	return rpc.Dial(addr, "org", rpc.Options{DefaultTimeout: timeout})
}
