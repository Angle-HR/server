package regionsaga

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/onboarding"
	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/region"
)

// OrgSide is org's half of the saga: the organization draft (accounts.pending_organizations) and the onboarding
// progress (accounts.pending_onboarding_progress) in the holding area, and their regional counterparts.
type OrgSide struct {
	Global   DB
	Regional func(region.Region) (DB, error)
}

var _ Org = (*OrgSide)(nil)

// CopyPending implements Org. Both inserts are idempotent.
func (o *OrgSide) CopyPending(ctx context.Context, userID uuid.UUID, target region.Region) (string, error) {
	pool, err := o.Regional(target)
	if err != nil {
		return "", Permanent{fmt.Errorf("org: region %q has no database: %w", target, err)}
	}
	var (
		legalName string
		roleID    uuid.UUID
		haveOrg   bool
	)
	err = o.Global.QueryRow(ctx, `SELECT legal_name, company_role_id FROM accounts.pending_organizations WHERE owner_user_id = $1`, userID).
		Scan(&legalName, &roleID)
	switch {
	case err == nil:
		haveOrg = true
	case !errors.Is(err, pgx.ErrNoRows):
		return "", fmt.Errorf("org: read pending organization: %w", err)
	}
	step, completed := onboarding.InitialProgress()
	var progressUser uuid.UUID
	err = o.Global.QueryRow(ctx, `SELECT user_id, current_step, completed_steps FROM accounts.pending_onboarding_progress WHERE user_id = $1`, userID).
		Scan(&progressUser, &step, &completed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("org: read pending progress: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if haveOrg {
		if _, err = tx.Exec(ctx, `INSERT INTO accounts.organizations (owner_user_id, legal_name, company_role_id)
			VALUES ($1, $2, $3) ON CONFLICT (owner_user_id) DO NOTHING`, userID, legalName, roleID); err != nil {
			return "", fmt.Errorf("org: copy organization to %s: %w", target, err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts.onboarding_progress (user_id, current_step, completed_steps) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET current_step = EXCLUDED.current_step, completed_steps = EXCLUDED.completed_steps`,
		userID, step, completed); err != nil {
		return "", fmt.Errorf("org: copy progress to %s: %w", target, err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return o.companyID(ctx, pool, userID)
}

func (o *OrgSide) companyID(ctx context.Context, pool DB, userID uuid.UUID) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `SELECT id::text FROM accounts.organizations WHERE owner_user_id = $1`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// UndoCopy implements Org: it deletes the regional organization and progress of the user. Only called while the
// registry still points at the holding area, so nothing else can have used them.
func (o *OrgSide) UndoCopy(ctx context.Context, userID uuid.UUID, target region.Region) error {
	pool, err := o.Regional(target)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.organizations WHERE owner_user_id = $1`, userID); err != nil {
		return fmt.Errorf("org: undo organization copy: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.onboarding_progress WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("org: undo progress copy: %w", err)
	}
	return tx.Commit(ctx)
}

// ReleasePending implements Org.
func (o *OrgSide) ReleasePending(ctx context.Context, userID uuid.UUID) error {
	tx, err := o.Global.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.pending_organizations WHERE owner_user_id = $1`, userID); err != nil {
		return fmt.Errorf("org: release pending organization: %w", err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM accounts.pending_onboarding_progress WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("org: release pending progress: %w", err)
	}
	return tx.Commit(ctx)
}

// Finalize implements Org: org.region.moved.v1 goes into the outbox of the target region (a no-op unless ORG_EVENTS
// is on). Its id derives from the user and region, so a repeat is dropped by the outbox.
func (o *OrgSide) Finalize(ctx context.Context, s Saga) error {
	if !orgevents.Enabled() {
		return nil
	}
	pool, err := o.Regional(s.Target)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	p := orgevents.RegionMoved{
		SagaID: s.ID.String(), UserID: s.UserID.String(), FromRegion: string(region.RegionGlobal), ToRegion: string(s.Target),
		MovedAt: time.Now().UTC(),
	}
	if id, cerr := o.companyID(ctx, tx, s.UserID); cerr != nil {
		return cerr
	} else if id != "" {
		p.CompanyID = id
		if p.CompanyKind, err = orgevents.KindOf(ctx, tx, id); err != nil {
			return err
		}
	}
	if err = orgevents.EnqueueRegionMoved(ctx, tx, p); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
