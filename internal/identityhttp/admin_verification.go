package identityhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/internal/verifyqueue"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

const kybMaxBodyBytes = 1 << 20

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
	// CaseType is "business" (company verification) or "individual" (identity review of a personal organization;
	// label it in the console).
	CaseType string `json:"case_type" enums:"business,individual"`
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
	CaseType           string            `json:"case_type" enums:"business,individual"`
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
//	@Security		AdminSessionCookie
//	@Param			limit	query		int	false	"Page size (max 100)"
//	@Param			offset	query		int	false	"Offset"
//	@Success		200		{object}	identityhttp.AdminVerificationListEnvelope
//	@Failure		401		{object}	apidoc.ErrorEnvelope
//	@Failure		403		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification [get]
func (h *AdminHandler) listVerificationQueue(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r.URL.Query().Get("limit"), defaultPageLimit, maxPageLimit)
	offset := parseLimit(r.URL.Query().Get("offset"), 0, maxPageOffset)

	rows, err := h.Queue.List(r.Context(), "", limit, offset)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	items := make([]AdminVerificationItem, 0, len(rows))
	for _, it := range rows {
		items = append(items, AdminVerificationItem{
			ID: it.ID, OrganizationID: it.OrganizationID, Region: it.Region, CountryCode: it.CountryCode,
			SubmittedAt: it.SubmittedAt, CaseType: it.CaseType,
		})
	}
	response.Success(w, r, http.StatusOK, items)
}

// verificationTarget is a pending queue item resolved to its region.
type verificationTarget struct {
	organizationID string
	region         region.Region
	submittedAt    time.Time
	caseType       string
}

// pendingVerification finds the organization's open review item. It writes the
// response and returns false when the id is invalid or there is no open item.
func (h *AdminHandler) pendingVerification(w http.ResponseWriter, r *http.Request) (*verificationTarget, bool) {
	orgID := chi.URLParam(r, "organizationID")
	if _, err := uuid.Parse(orgID); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid organization id"))
		return nil, false
	}
	item, err := h.Queue.Pending(r.Context(), orgID)
	if errors.Is(err, verifyqueue.ErrNoPendingItem) {
		response.Error(w, r, apperror.ErrNotFound)
		return nil, false
	}
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	reg, err := region.ParseRegion(item.Region)
	if err != nil {
		response.Error(w, r, err)
		return nil, false
	}
	return &verificationTarget{organizationID: orgID, region: reg, submittedAt: item.SubmittedAt, caseType: item.CaseType}, true
}

// getVerificationItem godoc
//
//	@Summary		Get a pending company verification review
//	@Description	Returns the submitted details from the organization's region so the operator can compare them with the government portal.
//	@Tags			admin/verification
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			organizationID	path		string	true	"Organization ID"
//	@Success		200				{object}	identityhttp.AdminVerificationDetailEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification/{organizationID} [get]
func (h *AdminHandler) getVerificationItem(w http.ResponseWriter, r *http.Request) {
	if h.Org == nil {
		response.Error(w, r, errUnavailable)
		return
	}
	t, ok := h.pendingVerification(w, r)
	if !ok {
		return
	}
	cd, err := h.Org.Detail(r.Context(), t.organizationID, t.region)
	if errors.Is(err, ErrDetailUnavailable) {
		response.Error(w, r, errUnavailable)
		return
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, verificationDetail(t, cd))
}

func verificationDetail(t *verificationTarget, cd CaseDetail) AdminVerificationDetail {
	return AdminVerificationDetail{
		OrganizationID: cd.CompanyID, Region: string(t.region), CaseType: t.caseType, CountryCode: cd.CountryCode, Status: cd.Status,
		Tier: cd.Tier, RegistrationNumber: cd.RegistrationNumber, LegalName: cd.LegalName, Address: cd.Address,
		AddressType: cd.AddressType, Identifiers: cd.Identifiers, Attempts: cd.Attempts, SubmittedAt: t.submittedAt,
	}
}

// reviewVerification godoc
//
//	@Summary		Approve or reject a company verification
//	@Description	Records the operator's decision. A rejection needs a valid failure reason and emails the owner.
//	@Tags			admin/verification
//	@Accept			json
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			organizationID	path		string								true	"Organization ID"
//	@Param			body			body		identityhttp.AdminVerificationReviewRequest	true	"Decision"
//	@Success		200				{object}	identityhttp.AdminVerificationReviewEnvelope
//	@Failure		400				{object}	apidoc.ErrorEnvelope
//	@Failure		404				{object}	apidoc.ErrorEnvelope
//	@Failure		409				{object}	apidoc.ErrorEnvelope
//	@Router			/admin/verification/{organizationID}/review [post]
func (h *AdminHandler) reviewVerification(w http.ResponseWriter, r *http.Request) {
	var req AdminVerificationReviewRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, kybMaxBodyBytes)).Decode(&req); err != nil ||
		(req.Decision != reviewDecisionApprove && req.Decision != reviewDecisionReject) {
		response.Error(w, r, apperror.InvalidBody(err))
		return
	}
	reviewerID, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	if h.Org == nil {
		response.Error(w, r, errUnavailable)
		return
	}
	t, ok := h.pendingVerification(w, r)
	if !ok {
		return
	}
	out, err := h.ReviewCase(r.Context(), t.organizationID, t.region, reviewerID.String(),
		req.Decision == reviewDecisionApprove, req.Reason)
	if out.Status != "" {
		// The decision is stored even when closing the queue item failed, so it is audited either way.
		h.audit(r, "verification.review."+req.Decision, auditVerificationType, t.organizationID,
			map[string]any{"region": string(t.region), "reason": req.Reason, "status": out.Status})
	}
	if err != nil {
		response.Error(w, r, err)
		return
	}
	response.Success(w, r, http.StatusOK, AdminVerificationReviewData{
		OrganizationID: t.organizationID, Status: out.Status, FailureReason: out.FailureReason,
	})
}

// ReviewCase applies a reviewer's decision: org records it on the company's KYB case (and notifies the owner of a
// rejection), then identity closes the queue item. The Outcome is set whenever org applied the decision, even when
// closing the item failed afterwards. It is shared by the HTTP route and the gRPC ReviewVerification.
func (h *AdminHandler) ReviewCase(
	ctx context.Context, companyID string, reg region.Region, reviewerID string, approve bool, reason string,
) (Outcome, error) {
	out, err := h.Org.Apply(ctx, Decision{
		CompanyID: companyID, Region: reg, ReviewerID: reviewerID, Approve: approve, Reason: reason,
	})
	if err != nil {
		return Outcome{}, err
	}
	if err := h.Queue.Resolve(ctx, companyID, reviewerID, approve); err != nil {
		return out, err
	}
	return out, nil
}
