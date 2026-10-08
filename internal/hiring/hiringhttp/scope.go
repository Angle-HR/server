package hiringhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/rbac"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// ErrNoOrganization means the signed-in user does not belong to a company yet.
var ErrNoOrganization = errors.New("handler: no organization for this account")

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
	// Now is for tests; nil uses the clock.
	Now func() time.Time
}

// The user's company: the one they own, else one they are a member of. Roles come from the member's role rows;
// an owner is always a founder, so a company that has not assigned roles yet still works.
const orgForUserSQL = `
SELECT o.id::text, o.legal_name, (o.owner_user_id = $1::uuid) AS is_owner,
       coalesce(array_agg(r.role) FILTER (WHERE r.role IS NOT NULL), '{}')::text[]
FROM accounts.organizations o
LEFT JOIN accounts.organization_members m ON m.organization_id = o.id AND m.user_id = $1::uuid
LEFT JOIN accounts.organization_member_roles r ON r.member_id = m.id
WHERE o.owner_user_id = $1::uuid OR m.user_id = $1::uuid
GROUP BY o.id, o.legal_name, o.owner_user_id, o.created_at
ORDER BY (o.owner_user_id = $1::uuid) DESC, o.created_at
LIMIT 1`

// PermissionsFor merges the default permissions of every role a person holds.
func PermissionsFor(roles []string, owner bool) rbac.Set {
	var perms []rbac.Permission
	seen := map[rbac.Role]bool{}
	add := func(r rbac.Role) {
		if !seen[r] {
			seen[r] = true
			perms = append(perms, rbac.DefaultMatrix[r]...)
		}
	}
	if owner {
		add(rbac.RoleFounder)
	}
	for _, r := range roles {
		add(rbac.Role(r))
	}
	return rbac.NewSet(perms...)
}

// Scope implements HiringDirectory.
func (d *PostgresHiringDirectory) Scope(
	ctx context.Context, userID string, reg region.Region,
) (*draft.Service, draft.Caller, error) {
	if reg == region.RegionGlobal {
		return nil, draft.Caller{}, errOnboardingIncomplete
	}
	pool, err := d.Regional(reg)
	if err != nil {
		return nil, draft.Caller{}, err
	}
	var (
		orgID, name string
		owner       bool
		roles       []string
	)
	err = pool.QueryRow(ctx, orgForUserSQL, userID).Scan(&orgID, &name, &owner, &roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, draft.Caller{}, ErrNoOrganization
	}
	if err != nil {
		return nil, draft.Caller{}, err
	}
	svc := &draft.Service{
		Store: &hiringstore.Store{DB: pool},
		Ref:   &hiringstore.Global{DB: d.Global},
		Now:   d.Now,
	}
	return svc, draft.Caller{UserID: userID, OrgID: orgID, CompanyName: name, Perms: PermissionsFor(roles, owner)}, nil
}

var errOnboardingIncomplete = apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete)

// Compile-time checks that the real stores satisfy the service's interfaces.
var (
	_ draft.Store     = (*hiringstore.Store)(nil)
	_ draft.Reference = (*hiringstore.Global)(nil)
)

// HiringHandler serves job drafts, the application form builder and the hiring catalogs.
type HiringHandler struct {
	Directory HiringDirectory
	Identify  HiringIdentify
}

// NewHiringHandler returns a handler that resolves callers through dir.
func NewHiringHandler(dir HiringDirectory, identify HiringIdentify) *HiringHandler {
	return &HiringHandler{Directory: dir, Identify: identify}
}

const hiringMaxBodyBytes = 1 << 20

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
		}
		response.Error(w, r, err)
		return nil, draft.Caller{}, false
	}
	return svc, c, true
}

// ifMatch reads the optional If-Match header, which carries the revision the client loaded.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	raw = strings.TrimPrefix(raw, "W/\"")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "If-Match must be the job revision"))
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
			response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		}
		return nil, false
	}
	return raw, true
}

// hiringError maps a service or store error to an API error.
func hiringError(err error) error {
	var (
		ve    *draft.ValidationError
		fe    *draft.ForbiddenError
		stale *hiringtypes.StaleRevisionError
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
	case errors.Is(err, hiringtypes.ErrNotFound):
		return apperror.New(apperror.CodeNotFound, "not found")
	case errors.Is(err, draft.ErrNotEditable), errors.Is(err, hiringtypes.ErrNotDraft):
		return apperror.New(apperror.CodeConflict, "only draft jobs can be changed here")
	case errors.Is(err, hiringtypes.ErrConflict):
		return apperror.New(apperror.CodeConflict, "already exists")
	}
	return err
}
