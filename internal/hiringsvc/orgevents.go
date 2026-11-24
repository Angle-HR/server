package hiringsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/draft"
	"github.com/Angle-HR/server/pkg/events"
	"github.com/Angle-HR/server/pkg/events/inbox"
)

// Org events: with HIRING_ORG_EVENTS=on the service consumes org.company.deleted.v1 and archives the company's live
// jobs. Consumer and inbox name: hiring-org.
const (
	EnvOrgEvents       = "HIRING_ORG_EVENTS"
	OrgConsumer        = "hiring-org"
	OrgSubjectFilter   = "org.company.deleted.>"
	typeCompanyDeleted = "org.company.deleted.v1"
)

var orgUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// CompanyDeletion archives a deleted company's jobs. Drafts, forms and templates stay: hiring keeps its own audit
// and compliance rows, and nothing public points at a draft.
type CompanyDeletion struct {
	Pools   jobmodel.Pools
	Service func(region string) (*draft.Service, error)
	Log     *slog.Logger
}

// Handle is the events.Handler.
func (c *CompanyDeletion) Handle(ctx context.Context, e events.Event) error {
	if e.Type != typeCompanyDeleted {
		return events.Terminal(fmt.Errorf("hiring-org: unexpected event type %q", e.Type))
	}
	var p struct {
		CompanyID string `json:"company_id"`
		Region    string `json:"region"`
		Reason    string `json:"reason"`
	}
	if err := e.DecodeJSON(&p); err != nil {
		return events.Terminal(fmt.Errorf("hiring-org: decode %s: %w", e.ID, err))
	}
	if p.CompanyID == "" {
		p.CompanyID = e.CompanyID
	}
	if p.Region == "" {
		p.Region = e.Region
	}
	if !orgUUID.MatchString(p.CompanyID) || p.Region == "" {
		return events.Terminal(fmt.Errorf("hiring-org: %s: company id %q or region %q invalid", e.ID, p.CompanyID, p.Region))
	}
	db, err := c.Pools(p.Region)
	if errors.Is(err, jobmodel.ErrUnknownRegion) {
		return events.Terminal(fmt.Errorf("hiring-org: %s: region %q has no database", e.ID, p.Region))
	}
	if err != nil {
		return fmt.Errorf("hiring-org: database for %q: %w", p.Region, err)
	}
	svc, err := c.Service(p.Region)
	if err != nil {
		return fmt.Errorf("hiring-org: service for %q: %w", p.Region, err)
	}
	// The archive runs in the service's own transactions and is repeatable; the inbox row only stops a redelivery
	// from doing the (then empty) sweep again.
	return inbox.Handle(db, OrgConsumer, func(ctx context.Context, _ pgx.Tx, _ events.Event) error {
		n, err := svc.ArchiveLiveJobs(ctx, p.CompanyID)
		if err != nil {
			return fmt.Errorf("hiring-org: archive jobs of %s: %w", p.CompanyID, err)
		}
		if c.Log != nil {
			c.Log.Info("company deleted: jobs archived", "company", p.CompanyID, "region", p.Region, "jobs", n, "reason", p.Reason)
		}
		return nil
	})(ctx, e)
}

// SubscribeOrgEvents starts the consumer. Stop the returned subscription on shutdown.
func SubscribeOrgEvents(ctx context.Context, bus events.Subscriber, router *dbrouter.DBRouter,
	service func(region string) (*draft.Service, error), log *slog.Logger, opts ...events.SubscribeOption,
) (events.Subscription, error) {
	h := &CompanyDeletion{Pools: jobmodel.RouterPools(router), Service: service, Log: log}
	return bus.Subscribe(ctx, OrgConsumer, []string{OrgSubjectFilter}, h.Handle, opts...)
}
