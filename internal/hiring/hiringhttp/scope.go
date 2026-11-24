package hiringhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Angle-HR/server/internal/hiring/apphttp"
	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/hiring/callerresolver"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/orgclient"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/rbac"
	"github.com/Angle-HR/server/pkg/response"
)

// ErrNoOrganization means the signed-in user does not belong to a company yet.
var ErrNoOrganization = apphttp.ErrNoOrganization

// HiringDirectory resolves who is calling and gives back the draft service bound to their region.
type HiringDirectory interface {
	Scope(ctx context.Context, userID string, reg region.Region) (*draft.Service, draft.Caller, error)
}

// HiringIdentify reads the signed-in user from the request context (auth.UserFromContext, adapted by app.go).
type HiringIdentify func(ctx context.Context) (userID string, reg region.Region, ok bool)

// RegionalDB returns the database pool of a region.
type RegionalDB func(reg region.Region) (hiringstore.DB, error)

// PostgresHiringDirectory is the production HiringDirectory.
type PostgresHiringDirectory struct {
	Regional RegionalDB
	Global   hiringstore.DB
	// Counts supplies the applicant counters of the job list. Nil reads them from the regional database.
	Counts draft.ApplicantCounts
	// Now is for tests; nil uses the clock.
	Now func() time.Time
	// Members resolves the caller's company and permissions. Nil reads the accounts tables (callerresolver);
	// the hiring service sets an OrgService.ResolveMembership client when ORG_MEMBERSHIP=grpc.
	Members orgclient.MembershipSource
	// Readiness answers the publish readiness (KYB, DPA, privacy contact). Nil reads the accounts tables;
	// ORG_READINESS=grpc sets an OrgService.GetPublishReadiness client and publishing fails closed on it.
	Readiness orgclient.ReadinessSource
	// Reference overrides the global reference data (ORG_INDUSTRIES=grpc wraps it). Nil uses hiringstore.Global.
	Reference draft.Reference
	// Company replaces the accounts.* reads and writes of the company row (ORG_COMPANY=grpc); Users resolves the
	// hiring managers' names (IDENTITY_USERS=grpc). Nil reads the shared tables.
	Company hiringstore.CompanyOrg
	Users   hiringstore.UserNames
}

// Service is the draft service bound to a region's database, with the configured readiness, reference and company
// adapters. Scope uses it after resolving the caller; the gRPC server uses it with the CallContext caller.
func (d *PostgresHiringDirectory) Service(reg region.Region) (*draft.Service, error) {
	pool, err := d.Regional(reg)
	if err != nil {
		return nil, err
	}
	store := &hiringstore.Store{DB: pool, Region: string(reg), Org: d.Company, Users: d.Users}
	var ref draft.Reference = &hiringstore.Global{DB: d.Global}
	if d.Reference != nil {
		ref = d.Reference
	}
	var company draft.Company = storeCompany{store: store}
	if d.Readiness != nil {
		company = orgclient.Company{Source: d.Readiness, DPIARecorded: store.DPIARecorded}
	}
	svc := &draft.Service{Store: store, Ref: ref, Now: d.Now, Company: company, Applicants: d.Counts}
	if svc.Applicants == nil {
		svc.Applicants = dbCounts{store: store}
	}
	return svc, nil
}

// PermissionsFor merges the default permissions of every role a person holds.
func PermissionsFor(roles []string, owner bool) rbac.Set {
	return callerresolver.PermissionsFor(roles, owner)
}

// Scope implements HiringDirectory. The company lookup is the interim accounts-table adapter
// (callerresolver) shared with the applications service.
func (d *PostgresHiringDirectory) Scope(
	ctx context.Context, userID string, reg region.Region,
) (*draft.Service, draft.Caller, error) {
	var members orgclient.MembershipSource = d.Members
	if members == nil {
		members = &callerresolver.Resolver{Regional: func(r region.Region) (callerresolver.DB, error) {
			p, err := d.Regional(r)
			if err != nil {
				return nil, err
			}
			return p, nil
		}}
	}
	m, err := members.Lookup(ctx, userID, reg)
	if err != nil {
		return nil, draft.Caller{}, err
	}
	svc, err := d.Service(reg)
	if err != nil {
		return nil, draft.Caller{}, err
	}
	return svc, draft.Caller{
		UserID: userID, OrgID: m.OrgID, Region: string(reg), CompanyName: m.CompanyName, CompanyAddress: m.CompanyAddress,
		Perms: m.Perms, Roles: m.Roles,
	}, nil
}

// dbCounts is the default applicant counter source: the shared table of the caller's region (what
// applicantcounts.DB does for a given region; kept here so a directory built without Counts behaves as before).
type dbCounts struct{ store *hiringstore.Store }

func (c dbCounts) ApplicantCounts(ctx context.Context, cl draft.Caller, ids []string) (map[string]draft.ApplicantCount, error) {
	rows, err := c.store.ApplicantCounts(ctx, cl.OrgID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]draft.ApplicantCount, len(rows))
	for _, r := range rows {
		out[r.JobID] = draft.ApplicantCount{Total: r.Total, New: r.New}
	}
	return out, nil
}

// storeCompany reads the company's publish prerequisites for the publish checks.
type storeCompany struct{ store *hiringstore.Store }

func (c storeCompany) State(ctx context.Context, orgID string) (draft.CompanyState, error) {
	return c.store.CompanyState(ctx, orgID, draft.CurrentDPAVersion)
}

// Compile-time checks that the real stores satisfy the service's interfaces.
var (
	_ draft.Store     = (*hiringstore.Store)(nil)
	_ draft.Reference = (*hiringstore.Global)(nil)
	_ draft.Company   = storeCompany{}
)

// HiringHandler serves job drafts, the application form builder and the hiring catalogs.
type HiringHandler struct {
	Directory HiringDirectory
	Identify  HiringIdentify
	// Public builds public job links for the share kit. Nil leaves the share kit unavailable.
	Public *public.Service
	// Applications returns a region's applications service. Nil leaves the applications routes answering 503.
	Applications RegionalApplications
	// Remote leaves every hiring route to the hiring service (HIRING_REMOTE=on on the monolith); only the
	// applications routes are still mounted, unless they are remote too.
	Remote bool
	// NoApplications is set by the hiring service: the applications routes belong to the applications service.
	NoApplications bool
}

// servesApplications reports whether this process mounts the applications routes.
func (h *HiringHandler) servesApplications() bool { return !h.NoApplications && !applicationsRemote() }

// RegionalApplications returns the applications service of a region.
type RegionalApplications = apphttp.RegionalApplications

// appRoutes returns the shared applications handlers, resolving callers through the hiring directory.
func (h *HiringHandler) appRoutes() *apphttp.Handler {
	return apphttp.New(apphttp.Deps{
		Identify:     apphttp.Identify(h.Identify),
		Callers:      directoryCallers{h.Directory},
		Applications: h.Applications,
	})
}

// directoryCallers adapts the hiring directory to apphttp.CallerResolver.
type directoryCallers struct{ dir HiringDirectory }

func (d directoryCallers) ResolveCaller(ctx context.Context, userID string, reg region.Region) (applications.Caller, error) {
	_, c, err := d.dir.Scope(ctx, userID, reg)
	if err != nil {
		return applications.Caller{}, err
	}
	return applications.Caller{UserID: c.UserID, OrgID: c.OrgID, Region: c.Region, Perms: c.Perms, Roles: c.Roles}, nil
}

// NewHiringHandler returns a handler that resolves callers through dir.
func NewHiringHandler(dir HiringDirectory, identify HiringIdentify) *HiringHandler {
	return &HiringHandler{Directory: dir, Identify: identify}
}

const hiringMaxBodyBytes = 1 << 20

// companyRetryAfterSeconds is the Retry-After sent when the company check is unavailable.
const companyRetryAfterSeconds = 5

// scope resolves the caller and the service. It writes the error response itself when it fails.
func (h *HiringHandler) scope(w http.ResponseWriter, r *http.Request) (*draft.Service, draft.Caller, bool) {
	userID, reg, ok := h.Identify(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return nil, draft.Caller{}, false
	}
	svc, c, err := h.Directory.Scope(r.Context(), userID, reg)
	if err != nil {
		if errors.Is(err, ErrNoOrganization) {
			err = apperror.New(apperror.CodeNotFound, "no organization for this account")
		} else if errors.Is(err, orgclient.ErrUnavailable) {
			err = apperror.NewWithDetails(apperror.CodeServiceUnavailable, "the organization service is not reachable, try again",
				map[string]any{apperror.DetailRetryAfterSeconds: companyRetryAfterSeconds, "retryable": true})
		}
		response.Error(w, r, err)
		return nil, draft.Caller{}, false
	}
	return svc, c, true
}

// ifMatch reads the If-Match header, which carries the revision the client loaded. Every job write needs it:
// without it a stale form could overwrite someone else's change, or publish a draft that changed since it was
// reviewed, so a missing header is refused (428) rather than treated as "any revision".
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	raw = strings.TrimPrefix(raw, "W/\"")
	if raw == "" {
		response.Error(w, r, apperror.NewWithDetails(apperror.CodePreconditionRequired,
			"send the job revision you loaded in the If-Match header",
			map[string]any{"fields": []jobs.FieldError{{Path: "If-Match", Message: "the job revision is required"}}}))
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		response.Error(w, r, apperror.NewWithDetails(apperror.CodeValidationError, "If-Match must be the job revision",
			map[string]any{"fields": []jobs.FieldError{{Path: "If-Match", Message: "must be the job revision"}}}))
		return 0, false
	}
	return n, true
}

func setETag(w http.ResponseWriter, revision int) {
	if revision > 0 {
		w.Header().Set("ETag", `"`+strconv.Itoa(revision)+`"`)
	}
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, hiringMaxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			response.Error(w, r, apperror.New(apperror.CodePayloadTooLarge, apperror.MsgRequestBodyTooLarge))
		} else {
			response.Error(w, r, apperror.InvalidBody(err))
		}
		return nil, false
	}
	return raw, true
}

// hiringError maps a service or store error to an API error.
func hiringError(err error) error {
	var (
		ve      *draft.ValidationError
		fe      *draft.ForbiddenError
		stale   *hiringtypes.StaleRevisionError
		blocked *draft.BlockedError
	)
	switch {
	case errors.As(err, &ve):
		return apperror.NewWithDetails(apperror.CodeValidationError, "some fields need attention",
			map[string]any{"fields": ve.Fields})
	case errors.As(err, &fe):
		return apperror.New(apperror.CodeForbidden, fe.Msg)
	case errors.As(err, &stale):
		return apperror.NewWithDetails(apperror.CodeConflict,
			"this job was changed by someone else; reload it and try again", map[string]any{"current_revision": stale.Current})
	case errors.Is(err, draft.ErrCompanyUnavailable):
		return apperror.NewWithDetails(apperror.CodeServiceUnavailable,
			"we could not check your company's publishing requirements right now; nothing was published, try again in a moment",
			map[string]any{apperror.DetailRetryAfterSeconds: companyRetryAfterSeconds, "retryable": true})
	case errors.Is(err, hiringtypes.ErrNotFound):
		return apperror.New(apperror.CodeNotFound, "not found")
	case errors.As(err, &blocked):
		return apperror.NewWithDetails(apperror.CodePublishBlocked, "this job cannot be published yet",
			map[string]any{"blocking": blocked.Report.Blocking, "warnings": blocked.Report.Warnings,
				"gates": blocked.Report.Gates})
	case errors.Is(err, draft.ErrInvalidTransition):
		return apperror.New(apperror.CodeConflict, "this job's status does not allow that")
	case errors.Is(err, draft.ErrNotEditable), errors.Is(err, hiringtypes.ErrNotDraft):
		return apperror.New(apperror.CodeConflict, "only draft jobs can be changed here")
	case errors.Is(err, hiringtypes.ErrConflict):
		return apperror.New(apperror.CodeConflict, "already exists")
	}
	return err
}

// Access returns the user's company roles and permissions for /auth/me. A user with no company, or any lookup
// failure, gets nothing rather than an error, so /auth/me keeps working.
func (h *HiringHandler) Access(ctx context.Context, userID string, reg region.Region) (roles, permissions []string) {
	svc, c, err := h.Directory.Scope(ctx, userID, reg)
	if err != nil {
		return nil, nil
	}
	me := svc.Me(c)
	return me.Roles, me.Permissions
}
