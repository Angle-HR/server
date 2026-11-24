package hiringstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobevents"
	"github.com/Angle-HR/server/pkg/events/outbox"
)

// emitJobEvents writes the job event a change calls for into the regional outbox, in the transaction that made
// the change. It does nothing unless HIRING_EVENTS=on. An error aborts the whole change (fail closed).
func (s *Store) emitJobEvents(
	ctx context.Context, tx pgx.Tx, tenantID, id, from string, res *hiringtypes.UpdateResult,
) error {
	return emitJobEvent(ctx, tx, s.Region, from, res, func() (*jobevents.Input, error) {
		return s.loadJobEventInput(ctx, tx, tenantID, id)
	})
}

// emitMembersChanged writes hiring.job.members_changed.v1 when the change saved the job's team and
// HIRING_EVENTS=on. Like emitJobEvents it runs in the transaction of the change and an error aborts it.
func (s *Store) emitMembersChanged(
	ctx context.Context, tx pgx.Tx, tenantID, id string, res *hiringtypes.UpdateResult,
) error {
	return emitMembersChanged(ctx, tx, s.Region, res, func() (*jobevents.MembersPayload, error) {
		rec, err := loadRecord(ctx, tx, tenantID, id, false)
		if err != nil {
			return nil, err
		}
		p := &jobevents.MembersPayload{
			Version: int64(rec.Job.Revision), JobID: id, CompanyID: tenantID, Region: s.Region,
			Members: make([]jobevents.Member, 0, len(rec.Members)),
		}
		for _, m := range rec.Members {
			p.Members = append(p.Members, jobevents.Member{UserID: m.UserID, Role: m.Role})
		}
		return p, nil
	})
}

func emitMembersChanged(
	ctx context.Context, tx outbox.Execer, region string, res *hiringtypes.UpdateResult,
	load func() (*jobevents.MembersPayload, error),
) error {
	if !jobevents.Enabled() || res.Members == nil {
		return nil
	}
	if region == "" {
		return errors.New("hiringstore: job events are on but the store has no region")
	}
	p, err := load()
	if err != nil {
		return err
	}
	ev, err := jobevents.BuildMembers(*p, time.Now())
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// emitJobEvent decides, loads (only when an event is due) and enqueues. It is the one place the gate is checked.
func emitJobEvent(
	ctx context.Context, tx outbox.Execer, region, from string, res *hiringtypes.UpdateResult,
	load func() (*jobevents.Input, error),
) error {
	if !jobevents.Enabled() {
		return nil
	}
	to := from
	if res.Status != nil {
		to = res.Status.To
	}
	typ, reason, ok := jobevents.Decide(jobevents.Change{
		From: from, To: to, Content: res.Details != nil || res.Rules != nil || res.Decls != nil,
	})
	if !ok {
		return nil
	}
	if region == "" {
		return errors.New("hiringstore: job events are on but the store has no region")
	}
	in, err := load()
	if err != nil {
		return err
	}
	return enqueueJobEvent(ctx, tx, in, typ, reason)
}

func enqueueJobEvent(ctx context.Context, tx outbox.Execer, in *jobevents.Input, typ, reason string) error {
	in.Reason = reason
	now := time.Now()
	in.Now = now
	ev, err := jobevents.Build(typ, jobevents.Snapshot(*in), now)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, ev)
}

// loadJobEventInput reads everything the payload needs from the job's own region.
func (s *Store) loadJobEventInput(ctx context.Context, tx pgx.Tx, tenantID, id string) (*jobevents.Input, error) {
	rec, err := loadRecord(ctx, tx, tenantID, id, false)
	if err != nil {
		return nil, err
	}
	in := &jobevents.Input{
		Region: s.Region, CompanyID: tenantID, Job: rec.Job, Rules: rec.Rules, Decls: rec.Declarations,
	}
	var f jobevents.Form
	var raw []byte
	err = tx.QueryRow(ctx, frozenFormSQL, id, tenantID).Scan(&f.ID, &f.Version, &raw)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("hiringstore: job event form: %w", err)
	default:
		f.Schema = raw
		in.Form = &f
	}
	prof, err := s.companyProfile(ctx, tx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: job event company: %w", err)
	}
	in.Company.Name, in.Company.PrivacyEmail, in.Company.DPOContact = prof.Name, prof.PrivacyEmail, prof.DPOContact
	return in, nil
}

const liveJobIDsSQL = `
SELECT id::text FROM hiring.job_postings
WHERE tenant_id = $1::uuid AND status IN ('published', 'paused') AND deleted_at IS NULL
ORDER BY id`

// EnqueueLiveJobEvents enqueues, for every published or paused job of one company, the event for its current
// state (published or paused). It is the backfill: event ids derive from type, job id and version, so running it
// again is a no-op. The caller drains the outbox afterwards. It returns how many jobs it covered.
func (s *Store) EnqueueLiveJobEvents(ctx context.Context, tenantID string) (int, error) {
	if s.Region == "" {
		return 0, errors.New("hiringstore: backfill needs the store's region")
	}
	n := 0
	err := s.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, liveJobIDsSQL, tenantID)
		if err != nil {
			return fmt.Errorf("hiringstore: list live jobs: %w", err)
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			in, err := s.loadJobEventInput(ctx, tx, tenantID, id)
			if err != nil {
				return err
			}
			typ := jobevents.TypePublished
			if in.Job.Status == "paused" {
				typ = jobevents.TypePaused
			}
			if err = enqueueJobEvent(ctx, tx, in, typ, ""); err != nil {
				return err
			}
			// The team too: members_changed only fires when a team is saved, so a consumer that starts later
			// would otherwise never learn the teams of existing jobs.
			mp := &jobevents.MembersPayload{
				Version: int64(in.Job.Revision), JobID: id, CompanyID: tenantID, Region: s.Region, Members: []jobevents.Member{},
			}
			rec, err := loadRecord(ctx, tx, tenantID, id, false)
			if err != nil {
				return err
			}
			for _, m := range rec.Members {
				mp.Members = append(mp.Members, jobevents.Member{UserID: m.UserID, Role: m.Role})
			}
			mev, err := jobevents.BuildMembers(*mp, time.Now())
			if err != nil {
				return err
			}
			if err = outbox.Enqueue(ctx, tx, mev); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
