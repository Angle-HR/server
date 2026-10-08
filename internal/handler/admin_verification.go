package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

const (
	reviewDecisionApprove = "approve"
	reviewDecisionReject  = "reject"
	auditVerificationType = "organization_verification"
)

// AdminVerificationItem is one pending item in the manual review queue. It holds
// ids, region and country only: company details stay in the organization's region.
type AdminVerificationItem struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Region         string    `json:"region"`
	CountryCode    string    `json:"country_code"`
	SubmittedAt    time.Time `json:"submitted_at"`
}

// AdminVerificationListEnvelope wraps the review queue list.
type AdminVerificationListEnvelope struct {
	Data []AdminVerificationItem `json:"data"`
	Meta *apidoc.Meta            `json:"meta,omitempty"`
}

// AdminVerificationDetail is what an operator compares against the government portal.
type AdminVerificationDetail struct {
	OrganizationID     string            `json:"organization_id"`
	Region             string            `json:"region"`
	CountryCode        string            `json:"country_code"`
	Status             string            `json:"status"`
	Tier               int               `json:"tier"`
	RegistrationNumber string            `json:"registration_number"`
	LegalName          string            `json:"legal_name"`
	Address            KYBAddress        `json:"address"`
	AddressType        string            `json:"address_type,omitempty"`
	Identifiers        map[string]string `json:"identifiers,omitempty"`
	Attempts           int               `json:"attempts"`
	SubmittedAt        time.Time         `json:"submitted_at"`
}

// AdminVerificationDetailEnvelope wraps a review item detail.
type AdminVerificationDetailEnvelope struct {
	Data AdminVerificationDetail `json:"data"`
	Meta *apidoc.Meta            `json:"meta,omitempty"`
}

// AdminVerificationReviewRequest is the operator's decision. A rejection needs a reason.
type AdminVerificationReviewRequest struct {
	Decision string `json:"decision" enums:"approve,reject"`
	Reason   string `json:"reason,omitempty" enums:"number_not_found,name_mismatch,address_mismatch,inactive_entity,wrong_country"`
}

// AdminVerificationReviewData is the outcome of a review.
type AdminVerificationReviewData struct {
	OrganizationID string `json:"organization_id"`
	Status         string `json:"status" enums:"verified,failed"`
	FailureReason  string `json:"failure_reason,omitempty"`
}

// AdminVerificationReviewEnvelope wraps a review outcome.
type AdminVerificationReviewEnvelope struct {
	Data AdminVerificationReviewData `json:"data"`
	Meta *apidoc.Meta                `json:"meta,omitempty"`
}

// listVerificationQueue godoc
//
//	@Summary		List pending company verification reviews
//	@Tags			admin/verification
//	@Produce		json
//	@Security		BearerAuth
//	@Param			limit	query		int	false	"Page size (max 100)"
//	@Param			offset	query		int	false	"Offset"
//	@Success		200		{object}	handler.AdminVerificationListEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification [get]
func (h *AdminHandler) listVerificationQueue(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), defaultPageLimit, maxPageLimit)
	offset := parseLimit(r.URL.Query().Get("offset"), 0, maxPageOffset)

	rows, err := h.GlobalDB.Query(r.Context(), `
		SELECT id::text, organization_id::text, region, country_code, submitted_at
		FROM admin.verification_queue
		WHERE status = 'pending'
		ORDER BY submitted_at, id
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	defer rows.Close()

	items := []AdminVerificationItem{}
	for rows.Next() {
		var it AdminVerificationItem
		if err := rows.Scan(&it.ID, &it.OrganizationID, &it.Region, &it.CountryCode, &it.SubmittedAt); err != nil {
			response.Error(w, r, err)
			return
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, items)
}

// verificationTarget is a pending queue item resolved to its region.
type verificationTarget struct {
	organizationID string
	region         region.Region
	submittedAt    time.Time
}

// pendingVerification finds the organization's open review item. It writes the
// response and returns false when the id is invalid or there is no open item.
func (h *AdminHandler) pendingVerification(w http.ResponseWriter, r *http.Request) (*verificationTarget, bool) {
	orgID := chi.URLParam(r, "organizationID")
	if _, err := uuid.Parse(orgID); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid organization id"))
		return nil, false
	}
	var regionName string
	t := &verificationTarget{organizationID: orgID}
	err := h.GlobalDB.QueryRow(r.Context(), `
		SELECT region, submitted_at FROM admin.verification_queue
		WHERE organization_id = $1 AND status = 'pending'`, orgID).Scan(&regionName, &t.submittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		response.Error(w, r, apperror.ErrNotFound)
		return nil, false
	}
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	reg, err := region.ParseRegion(regionName)
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	t.region = reg
	return t, true
}

// reviewService builds a service bound to the item's region. Review runs against
// one region's store, so it is built per request.
func (h *AdminHandler) reviewService(t *verificationTarget) (*kyb.Service, *kybstore.Store, error) {
	pool, err := h.Router.DB(t.region)
	if err != nil {
		return nil, nil, err
	}
	store := &kybstore.Store{DB: pool}
	return &kyb.Service{
		Store:    store,
		Queue:    &kybstore.Queue{DB: h.GlobalDB, Region: string(t.region)},
		Registry: kyb.NewRegistry(),
		Notifier: h.KYBNotifierFor.notifier(t.region),
		Logger:   slog.Default(),
	}, store, nil
}

// getVerificationItem godoc
//
//	@Summary		Get a pending company verification review
//	@Description	Returns the submitted details from the organization's region so the operator can compare them with the government portal.
//	@Tags			admin/verification
//	@Produce		json
//	@Security		BearerAuth
//	@Param			organizationID	path		string	true	"Organization ID"
//	@Success		200				{object}	handler.AdminVerificationDetailEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification/{organizationID} [get]
func (h *AdminHandler) getVerificationItem(w http.ResponseWriter, r *http.Request) {
	t, ok := h.pendingVerification(w, r)
	if !ok {
		return
	}
	_, store, err := h.reviewService(t)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	rec, err := store.Load(r.Context(), t.organizationID)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	if rec == nil {
		response.Error(w, r, apperror.ErrNotFound)
		return
	}
	response.Success(w, r, http.StatusOK, verificationDetail(t, rec))
}

func verificationDetail(t *verificationTarget, rec *kyb.Record) AdminVerificationDetail {
	return AdminVerificationDetail{
		OrganizationID:     rec.OrganizationID,
		Region:             string(t.region),
		CountryCode:        rec.CountryCode,
		Status:             string(rec.Status),
		Tier:               rec.Tier,
		RegistrationNumber: rec.RegistrationNumber,
		LegalName:          rec.LegalName,
		Address: KYBAddress{Line1: rec.Address.Line1, Line2: rec.Address.Line2, City: rec.Address.City,
			Region: rec.Address.Region, PostCode: rec.Address.PostCode, Country: rec.Address.Country},
		AddressType: string(rec.AddressType),
		Identifiers: rec.Identifiers,
		Attempts:    rec.Attempts,
		SubmittedAt: t.submittedAt,
	}
}

// reviewVerification godoc
//
//	@Summary		Approve or reject a company verification
//	@Description	Records the operator's decision. A rejection needs a valid failure reason and emails the owner.
//	@Tags			admin/verification
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			organizationID	path		string								true	"Organization ID"
//	@Param			body			body		handler.AdminVerificationReviewRequest	true	"Decision"
//	@Success		200				{object}	handler.AdminVerificationReviewEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Failure		409				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification/{organizationID}/review [post]
func (h *AdminHandler) reviewVerification(w http.ResponseWriter, r *http.Request) {
	var req AdminVerificationReviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, kybMaxBodyBytes)).Decode(&req); err != nil ||
		(req.Decision != reviewDecisionApprove && req.Decision != reviewDecisionReject) {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, apperror.MsgInvalidRequestBody))
		return
	}
	reviewerID, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	t, ok := h.pendingVerification(w, r)
	if !ok {
		return
	}
	svc, _, err := h.reviewService(t)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	rec, err := h.review(r.Context(), svc, t, reviewerID.String(), &req)
	if rec != nil {
		// The decision is stored even when closing the queue item failed, so it is audited either way.
		h.audit(r, "verification.review."+req.Decision, auditVerificationType, t.organizationID,
			map[string]any{"region": string(t.region), "reason": req.Reason, "status": string(rec.Status)})
	}
	if err != nil {
		response.Error(w, r, kybError(err))
		return
	}
	response.Success(w, r, http.StatusOK, AdminVerificationReviewData{
		OrganizationID: t.organizationID, Status: string(rec.Status), FailureReason: string(rec.FailureReason),
	})
}

func (h *AdminHandler) review(
	ctx context.Context, svc *kyb.Service, t *verificationTarget, reviewerID string, req *AdminVerificationReviewRequest,
) (*kyb.Record, error) {
	return svc.Review(ctx, t.organizationID, reviewerID, req.Decision == reviewDecisionApprove,
		kyb.FailureReason(req.Reason))
}
