package hiringhttp

import (
	"net/http"

	"github.com/Angle-HR/server/internal/apidoc"
	"github.com/Angle-HR/server/internal/hiring/jobs"
	"github.com/Angle-HR/server/internal/hiring/public"
	"github.com/Angle-HR/server/pkg/apperror"
	"github.com/Angle-HR/server/pkg/response"
)

// ShareKitEnvelope is a successful share kit response.
type ShareKitEnvelope struct {
	Data public.ShareKit `json:"data"`
	Meta *apidoc.Meta    `json:"meta,omitempty"`
}

// shareKit godoc
//
//	@Summary		Share kit
//	@Description	The job's public link and ready-made share links for WhatsApp, X, Facebook, LinkedIn, Telegram and email. Each link carries its channel as src, so applications show which link brought the candidate. Available once the job has been published.
//	@Tags			jobs
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Job id"
//	@Success		200	{object}	hiringhttp.ShareKitEnvelope
//	@Failure		404	{object}	apidoc.ErrorEnvelope
//	@Failure		409	{object}	apidoc.ErrorEnvelope
//	@Router			/jobs/{id}/share-kit [get]
func (h *HiringHandler) shareKit(w http.ResponseWriter, r *http.Request) {
	svc, c, ok := h.scope(w, r)
	if !ok {
		return
	}
	if h.Public == nil {
		response.Error(w, r, apperror.New(apperror.CodeNotImplemented, "sharing is not configured"))
		return
	}
	v, err := svc.Get(r.Context(), c, h.jobID(r))
	if err != nil {
		response.Error(w, r, hiringError(err))
		return
	}
	if v.PublicID == "" || (v.Status != jobs.StatusPublished && v.Status != jobs.StatusPaused) {
		response.Error(w, r, apperror.New(apperror.CodeConflict, "publish the job to get its share links"))
		return
	}
	response.Success(w, r, http.StatusOK, public.BuildShareKit(h.Public.SiteURL, v.PublicID, v.Title, c.CompanyName))
}
