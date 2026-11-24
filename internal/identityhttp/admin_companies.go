package identityhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	orgv1 "github.com/Angle-HR/server/gen/openhr/org/v1"
	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/auth"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

var _ = apidoc.ErrorEnvelope{}

// CompanyDeleter is what an OrgReview may also do: delete a company in org (AdminOrgService.DeleteCompany). Only
// the gRPC implementation (ORG_APPLY=grpc) has it; identity never touches org's tables to delete.
type CompanyDeleter interface {
	DeleteCompany(ctx context.Context, companyID string, reg region.Region, adminID, reason string) (time.Time, bool, error)
}

// DeleteCompany implements CompanyDeleter over AdminOrgService.
func (o *OrgGRPC) DeleteCompany(ctx context.Context, companyID string, reg region.Region, adminID, reason string) (time.Time, bool, error) {
	resp, err := o.Client.DeleteCompany(ctx, &orgv1.DeleteCompanyRequest{
		CompanyId: companyID, Region: string(reg), AdminId: adminID, Reason: reason,
	})
	if err != nil {
		return time.Time{}, false, grpcToAppError(err)
	}
	return resp.GetDeletedAt().AsTime(), resp.GetAlreadyDeleted(), nil
}

// AdminDeleteCompanyRequest is the optional body of DELETE /admin/companies/{id}.
type AdminDeleteCompanyRequest struct {
	Reason string `json:"reason"`
}

// AdminDeleteCompanyData is the answer of DELETE /admin/companies/{id}.
type AdminDeleteCompanyData struct {
	CompanyID      string `json:"company_id"`
	DeletedAt      string `json:"deleted_at"`
	AlreadyDeleted bool   `json:"already_deleted"`
}

// AdminDeleteCompanyEnvelope wraps AdminDeleteCompanyData.
type AdminDeleteCompanyEnvelope struct {
	Data AdminDeleteCompanyData `json:"data"`
}

// deleteCompany godoc
//
//	@Summary		Delete a company
//	@Description	Superadmin only. Org marks the company deleted and emits org.company.deleted.v1; applications and hiring remove their data when they consume it. Repeating it is harmless. Needs ORG_APPLY=grpc.
//	@Tags			admin/companies
//	@Accept			json
//	@Produce		json
//	@Security		AdminSessionCookie
//	@Param			id		path		string	true	"Company ID"
//	@Param			region	query		string	true	"Company region (uk, us, eu, africa, asia)"
//	@Success		200		{object}	identityhttp.AdminDeleteCompanyEnvelope
//	@Failure		400		{object}	apidoc.ErrorEnvelope
//	@Failure		404		{object}	apidoc.ErrorEnvelope
//	@Router			/admin/companies/{id} [delete]
func (h *AdminHandler) deleteCompany(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "invalid company id"))
		return
	}
	reg, err := region.ParseRegion(r.URL.Query().Get("region"))
	if err != nil || reg == region.RegionGlobal {
		response.Error(w, r, apperror.New(apperror.CodeValidationError, "region is required: uk, us, eu, africa or asia"))
		return
	}
	var body AdminDeleteCompanyRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
			response.Error(w, r, apperror.InvalidBody(err))
			return
		}
	}
	adminID, _, _, ok := auth.AdminFromContext(r.Context())
	if !ok {
		response.Error(w, r, apperror.ErrUnauthorized)
		return
	}
	del, ok := h.Org.(CompanyDeleter)
	if !ok {
		response.Error(w, r, errUnavailable)
		return
	}
	at, already, err := del.DeleteCompany(r.Context(), id, reg, adminID.String(), body.Reason)
	if err != nil {
		response.Error(w, r, err)
		return
	}
	h.audit(r, "company.delete", "company", id, map[string]any{"region": string(reg), "reason": body.Reason, "already_deleted": already})
	response.Success(w, r, http.StatusOK, AdminDeleteCompanyData{
		CompanyID: id, DeletedAt: at.UTC().Format(time.RFC3339), AlreadyDeleted: already,
	})
}
