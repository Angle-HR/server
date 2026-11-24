package identityhttp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
)

// KYBAddress is a postal address.
type KYBAddress struct {
	Line1    string `json:"line1"`
	Line2    string `json:"line2,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	PostCode string `json:"post_code,omitempty"`
	Country  string `json:"country,omitempty"`
}

// CaseDetail is what an operator compares against the government portal.
type CaseDetail struct {
	CompanyID          string
	CountryCode        string
	Status             string
	Tier               int
	RegistrationNumber string
	LegalName          string
	Address            KYBAddress
	AddressType        string
	Identifiers        map[string]string
	Attempts           int
}

// Decision is a reviewer's verdict on a KYB case.
type Decision struct {
	CompanyID  string
	Region     region.Region
	ReviewerID string
	Approve    bool
	Reason     string
}

// Outcome is the case state after the decision.
type Outcome struct {
	Status        string // verified or failed
	FailureReason string
}

// ErrDetailUnavailable means the org adapter cannot show a case's details (the gRPC contract has no RPC for them).
var ErrDetailUnavailable = errors.New("identityhttp: case detail not available from org")

// OrgReview is how identity's admin module talks to org about a KYB case: read the case detail for the reviewer and
// apply the decision (OrgService.ApplyKybDecision). Implementations: OrgDB (interim, writes the shared regional
// database) and OrgGRPC (selected with ORG_APPLY=grpc).
type OrgReview interface {
	Detail(ctx context.Context, companyID string, reg region.Region) (CaseDetail, error)
	Apply(ctx context.Context, d Decision) (Outcome, error)
}

// KYBNotifierFactory builds the KYB email notifier of a region (nil when the region has no database).
type KYBNotifierFactory func(region.Region) kyb.Notifier

// OrgDB is the interim OrgReview: it runs the KYB review (kyb.Service.Review) against the company's regional
// database directly, with the same notifier org used, but without touching the review queue, which identity closes
// itself afterwards.
//
// INTERIM: replaced by OrgGRPC once org serves ApplyKybDecision; it reads and writes org's tables.
type OrgDB struct {
	Router      *dbrouter.DBRouter
	NotifierFor KYBNotifierFactory
}

// noQueue satisfies kyb.Queue: identity owns the queue and resolves it after org applied the decision.
type noQueue struct{}

func (noQueue) Enqueue(context.Context, string, string) error       { return nil }
func (noQueue) Resolve(context.Context, string, string, bool) error { return nil }

func (o *OrgDB) store(reg region.Region) (*kybstore.Store, error) {
	pool, err := o.Router.DB(reg)
	if err != nil {
		return nil, err
	}
	return &kybstore.Store{DB: pool}, nil
}

// Detail implements OrgReview.
func (o *OrgDB) Detail(ctx context.Context, companyID string, reg region.Region) (CaseDetail, error) {
	store, err := o.store(reg)
	if err != nil {
		return CaseDetail{}, err
	}
	rec, err := store.Load(ctx, companyID)
	if err != nil {
		return CaseDetail{}, err
	}
	if rec == nil {
		return CaseDetail{}, apperror.ErrNotFound
	}
	return CaseDetail{
		CompanyID: rec.OrganizationID, CountryCode: rec.CountryCode, Status: string(rec.Status), Tier: rec.Tier,
		RegistrationNumber: rec.RegistrationNumber, LegalName: rec.LegalName,
		Address: KYBAddress{Line1: rec.Address.Line1, Line2: rec.Address.Line2, City: rec.Address.City,
			Region: rec.Address.Region, PostCode: rec.Address.PostCode, Country: rec.Address.Country},
		AddressType: string(rec.AddressType), Identifiers: rec.Identifiers, Attempts: rec.Attempts,
	}, nil
}

// Apply implements OrgReview.
func (o *OrgDB) Apply(ctx context.Context, d Decision) (Outcome, error) {
	store, err := o.store(d.Region)
	if err != nil {
		return Outcome{}, err
	}
	svc := &kyb.Service{
		Store: store, Queue: noQueue{}, Registry: kyb.NewRegistry(), Logger: slog.Default(),
	}
	if o.NotifierFor != nil {
		svc.Notifier = o.NotifierFor(d.Region)
	}
	rec, err := svc.Review(ctx, d.CompanyID, d.ReviewerID, d.Approve, kyb.FailureReason(d.Reason))
	if err != nil {
		return Outcome{}, kybError(err)
	}
	return Outcome{Status: string(rec.Status), FailureReason: string(rec.FailureReason)}, nil
}

// kybError maps a verification-flow error to an API error.
func kybError(err error) error {
	var invalid *kyb.InvalidInputError
	switch {
	case errors.As(err, &invalid):
		fields := make([]map[string]string, 0, len(invalid.Fields))
		for _, f := range invalid.Fields {
			fields = append(fields, map[string]string{"path": f.Path, "message": f.Message})
		}
		return apperror.NewWithDetails(apperror.CodeValidationError, apperror.MsgInvalidRequest,
			map[string]any{"fields": fields})
	case errors.Is(err, kyb.ErrInvalidInput):
		return apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequest)
	case errors.Is(err, kyb.ErrNothingToRetry), errors.Is(err, kyb.ErrActionNotAllowed):
		return apperror.New(apperror.CodeConflict, "action not available for the current verification status")
	case errors.Is(err, kyb.ErrAlreadyVerified):
		return apperror.New(apperror.CodeConflict, "organization is already verified")
	case errors.Is(err, kyb.ErrReviewInProgress):
		return apperror.New(apperror.CodeConflict, "manual review is in progress")
	case errors.Is(err, kyb.ErrTemporarilyUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable, "verification temporarily unavailable, try again")
	default:
		return err
	}
}
