package jobmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/region"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

// Pools returns the database of a region. An error that is (or wraps) ErrUnknownRegion is permanent.
type Pools func(region string) (inbox.DB, error)

// ErrUnknownRegion is returned by Pools for a region the service has no database for.
var ErrUnknownRegion = errors.New("jobmodel: unknown region")

// RouterPools adapts the regional database router.
func RouterPools(router *dbrouter.DBRouter) Pools {
	return func(reg string) (inbox.DB, error) {
		p, err := router.DB(region.Region(reg))
		if errors.Is(err, dbrouter.ErrUnknownRegion) {
			return nil, ErrUnknownRegion
		}
		if err != nil {
			return nil, err
		}
		return p, nil
	}
}

// Projector applies hiring.job.* events to the read model.
type Projector struct {
	Pools Pools
	// Index, when set, receives the public id -> region and company mapping of every job event (the global index
	// LookupJob answers from). It is written before the regional row and is idempotent, so a retry repeats it.
	Index IndexWriter
	// Now is the clock for events without a parseable updated_at; defaults to time.Now.
	Now func() time.Time
}

// NewProjector returns a Projector over pools.
func NewProjector(pools Pools) *Projector { return &Projector{Pools: pools} }

const upsertSQL = `
INSERT INTO hiring.app_jobs (
    job_id, public_id, company_id, region, status, public_state, closing_date, closed_at, retention_months,
    job_number, job_code, title, lawful_basis, markets, form_id, form_version, form_schema, rules, declarations,
    company_name, privacy_email, dpo_contact, notice, close_reason, version, source, published_at, updated_at, applied_at,
    created_by, location_mode)
VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6, NULLIF($7, '')::date, $8, $9, $10, $11, $12, $13, $14, $15, $16,
        $17::jsonb, $18::jsonb, $19::jsonb, $20, $21, $22, $23::jsonb, $24, $25, $26, $27, $28, now(), NULLIF($29, '')::uuid, $30)
ON CONFLICT (job_id) DO UPDATE SET
    public_id = EXCLUDED.public_id, company_id = EXCLUDED.company_id, region = EXCLUDED.region,
    status = EXCLUDED.status, public_state = EXCLUDED.public_state, closing_date = EXCLUDED.closing_date,
    closed_at = EXCLUDED.closed_at, retention_months = EXCLUDED.retention_months, job_number = EXCLUDED.job_number,
    job_code = EXCLUDED.job_code, title = EXCLUDED.title, lawful_basis = EXCLUDED.lawful_basis,
    markets = EXCLUDED.markets, form_id = EXCLUDED.form_id, form_version = EXCLUDED.form_version,
    form_schema = EXCLUDED.form_schema, rules = EXCLUDED.rules, declarations = EXCLUDED.declarations,
    company_name = EXCLUDED.company_name, privacy_email = EXCLUDED.privacy_email, dpo_contact = EXCLUDED.dpo_contact,
    notice = EXCLUDED.notice, close_reason = EXCLUDED.close_reason, version = EXCLUDED.version,
    source = EXCLUDED.source, published_at = EXCLUDED.published_at, updated_at = EXCLUDED.updated_at,
    applied_at = now(), created_by = COALESCE(EXCLUDED.created_by, hiring.app_jobs.created_by),
    location_mode = EXCLUDED.location_mode
WHERE hiring.app_jobs.version < EXCLUDED.version
   OR (hiring.app_jobs.version = EXCLUDED.version AND hiring.app_jobs.created_by IS NULL AND EXCLUDED.created_by IS NOT NULL)`

const insertFormSQL = `
INSERT INTO hiring.app_job_forms (form_id, version, company_id, job_id, schema)
VALUES ($1, $2, $3::uuid, $4::uuid, $5::jsonb) ON CONFLICT (form_id, version) DO NOTHING`

// upsert writes r when it is newer than the stored row. It reports whether a row was written.
func upsert(ctx context.Context, tx pgx.Tx, r row) (bool, error) {
	var schema any
	if r.FormSchema != nil {
		schema = r.FormSchema
		// A frozen version never changes, so keep it even when the job row itself is stale.
		if _, err := tx.Exec(ctx, insertFormSQL, *r.FormID, r.FormVersion, r.CompanyID, r.JobID, r.FormSchema); err != nil {
			return false, fmt.Errorf("jobmodel: store form of job %s: %w", r.JobID, err)
		}
	}
	tag, err := tx.Exec(ctx, upsertSQL,
		r.JobID, r.PublicID, r.CompanyID, r.Region, r.Status, r.PublicState, r.ClosingDate, r.ClosedAt,
		r.RetentionMonths, r.JobNumber, r.JobCode, r.Title, r.LawfulBasis, r.Markets, r.FormID, r.FormVersion,
		schema, r.Rules, r.Declarations, r.CompanyName, r.PrivacyEmail, r.DPOContact, r.Notice, r.CloseReason,
		r.Version, r.Source, r.PublishedAt, r.UpdatedAt, r.CreatedBy, r.LocationMode)
	if err != nil {
		return false, fmt.Errorf("jobmodel: upsert job %s: %w", r.JobID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Handle is the events.Handler. Undecodable or invalid events and events of an unknown type or region are Terminal
// (dead-lettered); a database failure is retried.
func (p *Projector) Handle(ctx context.Context, e events.Event) error {
	if e.Type == TypeMembersChanged {
		return p.handleMembers(ctx, e)
	}
	if !knownType(e.Type) {
		return events.Terminal(fmt.Errorf("jobmodel: unexpected event type %q", e.Type))
	}
	var pl payload
	if err := e.DecodeJSON(&pl); err != nil {
		return events.Terminal(fmt.Errorf("jobmodel: decode %s %s: %w", e.Type, e.ID, err))
	}
	if pl.Region == "" {
		pl.Region = e.Region
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	r, err := pl.toRow(now())
	if err != nil {
		return events.Terminal(fmt.Errorf("jobmodel: invalid %s %s: %w", e.Type, e.ID, err))
	}
	db, err := p.Pools(r.Region)
	if errors.Is(err, ErrUnknownRegion) {
		return events.Terminal(fmt.Errorf("jobmodel: %s %s: region %q has no database", e.Type, e.ID, r.Region))
	}
	if err != nil {
		return fmt.Errorf("jobmodel: database for region %q: %w", r.Region, err)
	}
	if p.Index != nil {
		if err := p.Index.PutJob(ctx, r.PublicID, r.Region, r.CompanyID, r.JobID, r.Version); err != nil {
			return fmt.Errorf("jobmodel: index job %s: %w", r.JobID, err)
		}
	}
	return inbox.Handle(db, Consumer, func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		if deleted, err := companyDeleted(ctx, tx, r.CompanyID); err != nil || deleted {
			return err
		}
		_, err := upsert(ctx, tx, r)
		return err
	})(ctx, e)
}

const upsertMembersSQL = `
INSERT INTO hiring.app_job_members (job_id, company_id, version, members, updated_at)
VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, now())
ON CONFLICT (job_id) DO UPDATE SET company_id = EXCLUDED.company_id, version = EXCLUDED.version,
    members = EXCLUDED.members, updated_at = now()
WHERE hiring.app_job_members.version < EXCLUDED.version`

// handleMembers applies hiring.job.members_changed.v1: the team is replaced when the event's version is higher.
func (p *Projector) handleMembers(ctx context.Context, e events.Event) error {
	var pl membersPayload
	if err := e.DecodeJSON(&pl); err != nil {
		return events.Terminal(fmt.Errorf("jobmodel: decode %s %s: %w", e.Type, e.ID, err))
	}
	if pl.Region == "" {
		pl.Region = e.Region
	}
	switch {
	case pl.JobID == "" || pl.CompanyID == "":
		return events.Terminal(fmt.Errorf("jobmodel: invalid %s %s: job_id and company_id are required", e.Type, e.ID))
	case pl.Region == "":
		return events.Terminal(fmt.Errorf("jobmodel: invalid %s %s: region is required", e.Type, e.ID))
	case pl.Version < 1:
		return events.Terminal(fmt.Errorf("jobmodel: invalid %s %s: version %d is not positive", e.Type, e.ID, pl.Version))
	}
	team := make([]Member, 0, len(pl.Members))
	for _, m := range pl.Members {
		if m.UserID == "" {
			return events.Terminal(fmt.Errorf("jobmodel: invalid %s %s: member without user_id", e.Type, e.ID))
		}
		team = append(team, Member{UserID: m.UserID, Role: m.Role})
	}
	raw, err := json.Marshal(team)
	if err != nil {
		return events.Terminal(err)
	}
	db, err := p.Pools(pl.Region)
	if errors.Is(err, ErrUnknownRegion) {
		return events.Terminal(fmt.Errorf("jobmodel: %s %s: region %q has no database", e.Type, e.ID, pl.Region))
	}
	if err != nil {
		return fmt.Errorf("jobmodel: database for region %q: %w", pl.Region, err)
	}
	return inbox.Handle(db, Consumer, func(ctx context.Context, tx pgx.Tx, _ events.Event) error {
		if deleted, err := companyDeleted(ctx, tx, pl.CompanyID); err != nil || deleted {
			return err
		}
		if _, err := tx.Exec(ctx, upsertMembersSQL, pl.JobID, pl.CompanyID, pl.Version, raw); err != nil {
			return fmt.Errorf("jobmodel: store members of job %s: %w", pl.JobID, err)
		}
		return nil
	})(ctx, e)
}

// Subscribe starts the projector on every hiring.job.* event. Stop the returned subscription on shutdown.
func Subscribe(ctx context.Context, bus events.Subscriber, router *dbrouter.DBRouter, opts ...events.SubscribeOption) (events.Subscription, error) {
	return SubscribePools(ctx, bus, RouterPools(router), opts...)
}

// SubscribePools is Subscribe over any Pools (tests, a single database).
func SubscribePools(ctx context.Context, bus events.Subscriber, pools Pools, opts ...events.SubscribeOption) (events.Subscription, error) {
	return bus.Subscribe(ctx, Consumer, []string{SubjectFilter}, NewProjector(pools).Handle, opts...)
}

// SubscribeIndexed is Subscribe with the public id index fed as well (index may be nil).
func SubscribeIndexed(ctx context.Context, bus events.Subscriber, router *dbrouter.DBRouter, index IndexWriter, opts ...events.SubscribeOption) (events.Subscription, error) {
	p := NewProjector(RouterPools(router))
	p.Index = index
	return bus.Subscribe(ctx, Consumer, []string{SubjectFilter}, p.Handle, opts...)
}

// companyDeleted reports whether the company's applications data was deleted; its job events are then dropped.
func companyDeleted(ctx context.Context, tx pgx.Tx, companyID string) (bool, error) {
	var deleted bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM hiring.app_deleted_companies WHERE company_id = $1::uuid)`, companyID).Scan(&deleted)
	if err != nil {
		return false, fmt.Errorf("jobmodel: deleted company check: %w", err)
	}
	return deleted, nil
}
