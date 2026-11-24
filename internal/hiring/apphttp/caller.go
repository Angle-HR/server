package apphttp

import (
	"context"
	"errors"

	"github.com/Angle-HR/server/internal/hiring/applications"
	"github.com/Angle-HR/server/internal/region"
)

// ErrNoOrganization means the signed-in user does not belong to a company yet. A CallerResolver returns it
// (possibly wrapped) and the routes answer 404.
var ErrNoOrganization = errors.New("handler: no organization for this account")

// CallerResolver turns the authenticated session user into the applications.Caller the services act for: the
// company, the user, the region and the permissions the user holds in that company.
//
// It is the only place the applications side needs company and role data. Today the implementation reads the
// accounts tables (package callerresolver, an interim adapter owned by the monolith side). When org exposes
// membership over gRPC (docs/microservices/BOUNDARIES.md item 1) an org client implements this interface and the
// adapter is deleted; nothing in apphttp changes.
type CallerResolver interface {
	// ResolveCaller returns ErrNoOrganization when the user has no company, and an apperror (for example
	// onboarding_incomplete) when the account cannot act for one yet.
	ResolveCaller(ctx context.Context, userID string, reg region.Region) (applications.Caller, error)
}
