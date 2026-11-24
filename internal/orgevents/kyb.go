package orgevents

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/kyb"
	"github.com/Angle-HR/server/internal/kyb/kybstore"
)

// KYBSaveHook returns the kybstore hook that writes org.company.verified.v1 or org.company.kyb_rejected.v1 when the
// saved record is verified or failed. It returns nil when events are off, so callers can assign it unconditionally.
// region is the company's region.
func KYBSaveHook(region string) kybstore.SaveHook {
	if !Enabled() {
		return nil
	}
	return func(ctx context.Context, tx pgx.Tx, rec *kyb.Record) error {
		if rec.Status != kyb.StatusVerified && rec.Status != kyb.StatusFailed {
			return nil
		}
		kind, err := KindOf(ctx, tx, rec.OrganizationID)
		if err != nil {
			return err
		}
		switch rec.Status {
		case kyb.StatusVerified:
			at := time.Now().UTC()
			if rec.VerifiedAt != nil {
				at = *rec.VerifiedAt
			}
			return EnqueueVerified(ctx, tx, CompanyVerified{
				CompanyKind: kind, CompanyID: rec.OrganizationID, Region: region, Status: string(kyb.StatusVerified), VerifiedAt: at,
			})
		case kyb.StatusFailed:
			at := time.Now().UTC()
			if rec.CheckedAt != nil {
				at = *rec.CheckedAt
			}
			return EnqueueKybRejected(ctx, tx, CompanyKybRejected{
				CompanyKind: kind, CompanyID: rec.OrganizationID, Region: region, Status: string(kyb.StatusFailed),
				Reason: string(rec.FailureReason), RejectedAt: at,
			})
		}
		return nil
	}
}
