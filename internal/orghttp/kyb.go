package orghttp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Angle-HR/server/internal/countrycode"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybidentity"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/orgstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

const kybMaxBodyBytes = 1 << 20

// KYBHandler serves company verification (KYB V2) for the signed-in organization owner.
type KYBHandler struct {
	Router      *dbrouter.DBRouter
	GlobalDB    globalDB
	Registry    *kyb.Registry
	NotifierFor KYBNotifierFactory // optional
	Formats     kyb.FormatChecker  // optional
	// QueueFor builds the review queue of a region (IDENTITY_QUEUE, see kybidentity). Nil writes
	// admin.verification_queue in the global database, as the monolith always did.
	QueueFor kybidentity.Factory
}

// KYBNotifierFactory builds the notifier for one region. A nil factory means no emails.
type KYBNotifierFactory func(reg region.Region) kyb.Notifier

func (f KYBNotifierFactory) notifier(reg region.Region) kyb.Notifier {
	if f == nil {
		return nil
	}
	return f(reg)
}

// NewKYBHandler returns a KYB handler. registry decides which countries are verified automatically.
func NewKYBHandler(router *dbrouter.DBRouter, global globalDB, registry *kyb.Registry) *KYBHandler {
	return &KYBHandler{Router: router, GlobalDB: global, Registry: registry}
}

// RegisterProtectedRoutes mounts the authenticated verification routes.
func (h *KYBHandler) RegisterProtectedRoutes(r chi.Router) {
	r.Route("/organization/verification", func(r chi.Router) {
		r.Get("/", h.status)
		r.Post("/", h.submit)
		r.Post("/retry", h.retry)
		r.Post("/confirm-name", h.confirmName)
		r.Post("/confirm-address", h.confirmAddress)
		r.Post("/change-country", h.changeCountry)
	})
}

// KYBAddress is a postal address.
type KYBAddress struct {
	Line1    string `json:"line1"`
	Line2    string `json:"line2,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	PostCode string `json:"post_code,omitempty"`
	Country  string `json:"country,omitempty"`
}

// KYBSubmitRequest is the full verification form.
type KYBSubmitRequest struct {
	CountryCode        string            `json:"country_code"        example:"GB"`
	RegistrationNumber string            `json:"registration_number" example:"01234567"`
	LegalName          string            `json:"legal_name"          example:"Acme Ltd"`
	Address            KYBAddress        `json:"address"`
	AddressType        string            `json:"address_type,omitempty" enums:"registered,trading"`
	Identifiers        map[string]string `json:"identifiers,omitempty"`
}

// KYBRetryRequest is the short re-verification form (name and number only).
type KYBRetryRequest struct {
	LegalName          string `json:"legal_name"`
	RegistrationNumber string `json:"registration_number"`
}

// KYBConfirmAddressRequest says which kind of address the user entered.
type KYBConfirmAddressRequest struct {
	AddressType string `json:"address_type" enums:"registered,trading"`
}

// KYBChangeCountryRequest switches the country after a wrong-country failure.
type KYBChangeCountryRequest struct {
	CountryCode string `json:"country_code" example:"KE"`
}

// KYBStatusData is the verification state shown on the dashboard.
type KYBStatusData struct {
	// Kind is "business" (company verification) or "individual" (manual identity review of a personal organization).
	Kind              string `json:"kind"               enums:"business,individual"`
	Status            string `json:"status"             enums:"not_started,pending,verified,failed"`
	Display           string `json:"display"            enums:"verified,pending_review,action_required,failed,not_started"`
	FailureReason     string `json:"failure_reason,omitempty"`
	Tier              int    `json:"tier,omitempty"`
	CanPublish        bool   `json:"can_publish"`
	CanRetry          bool   `json:"can_retry"`
	LightRetry        bool   `json:"light_retry"`
	NameConfirmable   bool   `json:"name_confirmable"`
	AddressConfirm    bool   `json:"address_confirmable"`
	CountryCode       string `json:"country_code,omitempty"`
	RegistryName      string `json:"registry_name,omitempty"`
	RegisteredAddress string `json:"registered_address,omitempty"`
	CheckedAt         string `json:"checked_at,omitempty"`
	VerifiedAt        string `json:"verified_at,omitempty"`
}

// KYBStatusEnvelope is a successful verification status response.
type KYBStatusEnvelope struct {
	Data KYBStatusData `json:"data"`
	Meta *apidoc.Meta  `json:"meta,omitempty"`
}

func kybStatusData(rec *kyb.Record) KYBStatusData {
	if rec == nil {
		return KYBStatusData{Status: string(kyb.StatusNotStarted), Display: string(kyb.DisplayNotStarted)}
	}
	d := KYBStatusData{
		Status:            string(rec.Status),
		Display:           string(kyb.DisplayState(rec.Status, rec.FailureReason)),
		FailureReason:     string(rec.FailureReason),
		Tier:              rec.Tier,
		CanPublish:        rec.Status.CanPublish(),
		CountryCode:       rec.CountryCode,
		RegistryName:      rec.RegistryName,
		RegisteredAddress: rec.RegisteredAddress,
	}
	failed := rec.Status == kyb.StatusFailed
	d.CanRetry = failed && !rec.FailureReason.HardBlock()
	d.LightRetry = failed && rec.FailureReason.LightRetry()
	d.NameConfirmable = failed && rec.FailureReason == kyb.ReasonNameMismatch && rec.RegistryName != ""
	d.AddressConfirm = failed && rec.FailureReason.SoftWarning()
	if rec.CheckedAt != nil {
		d.CheckedAt = rec.CheckedAt.UTC().Format(time.RFC3339)
	}
	if rec.VerifiedAt != nil {
		d.VerifiedAt = rec.VerifiedAt.UTC().Format(time.RFC3339)
	}
	return d
}

// kybScope is everything a request needs: the user, their organization and a ready service.
type kybScope struct {
	userID string
	orgID  string
	kind   string
	reg    region.Region
	svc    *kyb.Service
	store  *kybstore.Store
}

// scope resolves the signed-in owner's organization and builds a service bound to their region.
func (h *KYBHandler) scope(w http.ResponseWriter, r *http.Request) (*kybScope, bool) {
	userID, reg, ok := auth.UserFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return nil, false
	}
	if reg == region.RegionGlobal {
		response.Error(w, r, apperror.New(apperror.CodeOnboardingIncomplete, apperror.MsgOnboardingStepIncomplete))
		return nil, false
	}
	pool, err := h.Router.DB(reg)
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	var orgID, kind string
	err = pool.QueryRow(r.Context(),
		`SELECT id::text, kind FROM accounts.organizations WHERE owner_user_id = $1 AND deleted_at IS NULL`, userID).Scan(&orgID, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		response.Error(w, r, apperror.New(apperror.CodeNotFound, "no organization for this account"))
		return nil, false
	}
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	store := &kybstore.Store{DB: pool, OnSave: orgevents.KYBSaveHook(string(reg))}
	return &kybScope{
		userID: userID.String(),
		orgID:  orgID,
		kind:   kind,
		reg:    reg,
		store:  store,
		svc: &kyb.Service{
			Store:     store,
			Queue:     h.queue(reg),
			Registry:  h.Registry,
			Notifier:  h.NotifierFor.notifier(reg),
			Formats:   h.Formats,
			Logger:    slog.Default(),
			Supported: countrycode.SupportedFromEnv(),
		},
	}, true
}

func (h *KYBHandler) queue(reg region.Region) kyb.CaseQueue {
	if h.QueueFor != nil {
		if q, ok := h.QueueFor(string(reg)).(kyb.CaseQueue); ok {
			return q
		}
	}
	return &kybstore.Queue{DB: h.GlobalDB, Region: string(reg)}
}

func decodeKYB(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, kybMaxBodyBytes)).Decode(dst); err != nil {
		response.Error(w, r, apperror.InvalidBody(err))
		return false
	}
	return true
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
	case errors.Is(err, kyb.ErrResubmissionBlocked):
		return apperror.New(apperror.CodeForbidden, "this company cannot be verified automatically, contact support")
	case errors.Is(err, kyb.ErrAlreadyVerified):
		return apperror.New(apperror.CodeConflict, "organization is already verified")
	case errors.Is(err, kyb.ErrReviewInProgress):
		return apperror.New(apperror.CodeConflict, "manual review is in progress")
	case errors.Is(err, kyb.ErrNotVerified):
		return apperror.New(apperror.CodeForbidden, "company must be verified before publishing")
	case errors.Is(err, kyb.ErrNothingToRetry), errors.Is(err, kyb.ErrActionNotAllowed):
		return apperror.New(apperror.CodeConflict, "action not available for the current verification status")
	case errors.Is(err, kyb.ErrTemporarilyUnavailable):
		return apperror.New(apperror.CodeServiceUnavailable, "verification temporarily unavailable, try again")
	default:
		return err
	}
}

func (h *KYBHandler) respond(w http.ResponseWriter, r *http.Request, sc *kybScope, rec *kyb.Record, err error) {
	if err != nil {
		response.Error(w, r, kybError(err))
		return
	}
	data := kybStatusData(rec)
	data.Kind = sc.kind
	if sc.kind == orgstore.KindIndividual {
		// An individual's review is a person's decision: nothing to retry, confirm or change by themselves.
		data.CanRetry, data.LightRetry, data.NameConfirmable, data.AddressConfirm = false, false, false, false
	}
	response.Success(w, r, http.StatusOK, data)
}

// errIndividualReview is answered to the company-registration actions on a personal organization.
var errIndividualReview = apperror.New(apperror.CodeConflict, "action not available for the current verification status")

func (h *KYBHandler) run(
	w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, sc *kybScope) (*kyb.Record, error),
) {
	h.runFor(w, r, true, fn)
}

// runFor is run with a switch for the actions a personal organization may not take (company registration).
func (h *KYBHandler) runFor(
	w http.ResponseWriter, r *http.Request, businessOnly bool, fn func(ctx context.Context, sc *kybScope) (*kyb.Record, error),
) {
	sc, ok := h.scope(w, r)
	if !ok {
		return
	}
	if businessOnly && sc.kind == orgstore.KindIndividual {
		response.Error(w, r, errIndividualReview)
		return
	}
	rec, err := fn(r.Context(), sc)
	h.respond(w, r, sc, rec, err)
}

// status godoc
//
//	@Summary		Get company verification status
//	@Tags			organization/verification
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	orghttp.KYBStatusEnvelope
//	@Failure		401	{object}	apidoc.ErrorEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification [get]
func (h *KYBHandler) status(w http.ResponseWriter, r *http.Request) {
	h.runFor(w, r, false, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		return sc.store.Load(ctx, sc.orgID)
	})
}

// submit godoc
//
//	@Summary		Submit company verification
//	@Description	First submission or a full resubmission after a failure. Tier 1 countries are checked immediately; others are queued for manual review (status pending).
//	@Tags			organization/verification
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.KYBSubmitRequest	true	"Verification form"
//	@Success		200		{object}	orghttp.KYBStatusEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		503		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification [post]
func (h *KYBHandler) submit(w http.ResponseWriter, r *http.Request) {
	var req KYBSubmitRequest
	if !decodeKYB(w, r, &req) {
		return
	}
	h.runFor(w, r, false, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		if sc.kind == orgstore.KindIndividual {
			return h.requestPersonalReview(ctx, sc)
		}
		return sc.svc.Submit(ctx, &kyb.SubmitInput{
			OrganizationID: sc.orgID, CountryCode: req.CountryCode, RegistrationNumber: req.RegistrationNumber,
			LegalName: req.LegalName, AddressType: kyb.AddressType(req.AddressType), Identifiers: req.Identifiers,
			Address: kyb.Address{Line1: req.Address.Line1, Line2: req.Address.Line2, City: req.Address.City,
				Region: req.Address.Region, PostCode: req.Address.PostCode, Country: req.Address.Country},
			ActorID: sc.userID,
		})
	})
}

// requestPersonalReview is POST /organization/verification for an individual: the body is ignored and the personal
// organization is put (back) in front of the reviewer. It is refused while a review is open and once verified.
func (h *KYBHandler) requestPersonalReview(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
	rec, err := sc.store.Load(ctx, sc.orgID)
	if err != nil {
		return nil, err
	}
	if rec != nil && rec.Status == kyb.StatusPending {
		return nil, kyb.ErrReviewInProgress
	}
	store := &orgstore.Store{Regional: func(region.Region) (orgstore.DB, error) {
		return h.Router.DB(sc.reg)
	}}
	return store.RequestPersonalReview(ctx, sc.reg, sc.orgID, sc.userID, h.queue(sc.reg))
}

// retry godoc
//
//	@Summary		Retry verification with name and number only
//	@Description	Light retry for number_not_found and name_mismatch failures. Country and address are reused.
//	@Tags			organization/verification
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.KYBRetryRequest	true	"Retry form"
//	@Success		200		{object}	orghttp.KYBStatusEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Failure		503		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification/retry [post]
func (h *KYBHandler) retry(w http.ResponseWriter, r *http.Request) {
	var req KYBRetryRequest
	if !decodeKYB(w, r, &req) {
		return
	}
	h.run(w, r, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		return sc.svc.Resubmit(ctx, sc.orgID, req.LegalName, req.RegistrationNumber, sc.userID)
	})
}

// confirmName godoc
//
//	@Summary		Confirm the registry's company name
//	@Description	One-click fix for a name_mismatch: the registry's legal name replaces what was typed and the check runs again.
//	@Tags			organization/verification
//	@Produce		json
//	@Security		SessionCookie
//	@Success		200	{object}	orghttp.KYBStatusEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Failure		503	{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification/confirm-name [post]
func (h *KYBHandler) confirmName(w http.ResponseWriter, r *http.Request) {
	h.run(w, r, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		return sc.svc.ConfirmName(ctx, sc.orgID, sc.userID)
	})
}

// confirmAddress godoc
//
//	@Summary		Confirm which address was entered
//	@Description	Accepts an address_mismatch warning once the user says whether the address is registered or trading.
//	@Tags			organization/verification
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.KYBConfirmAddressRequest	true	"Address type"
//	@Success		200		{object}	orghttp.KYBStatusEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification/confirm-address [post]
func (h *KYBHandler) confirmAddress(w http.ResponseWriter, r *http.Request) {
	var req KYBConfirmAddressRequest
	if !decodeKYB(w, r, &req) {
		return
	}
	h.run(w, r, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		return sc.svc.ConfirmAddress(ctx, sc.orgID, kyb.AddressType(req.AddressType), sc.userID)
	})
}

// changeCountry godoc
//
//	@Summary		Change the registration country
//	@Description	Clears the registration number and country identifiers, keeps name and address, and returns the status to not_started.
//	@Tags			organization/verification
//	@Accept			json
//	@Produce		json
//	@Security		SessionCookie
//	@Param			body	body		orghttp.KYBChangeCountryRequest	true	"Country"
//	@Success		200		{object}	orghttp.KYBStatusEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		409		{object}	apidoc.ErrorEnvelope
//	@Router			/organization/verification/change-country [post]
func (h *KYBHandler) changeCountry(w http.ResponseWriter, r *http.Request) {
	var req KYBChangeCountryRequest
	if !decodeKYB(w, r, &req) {
		return
	}
	h.run(w, r, func(ctx context.Context, sc *kybScope) (*kyb.Record, error) {
		return sc.svc.ChangeCountry(ctx, sc.orgID, req.CountryCode, sc.userID)
	})
}
