package applicationsvc

import (
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Angle-HR/server/internal/applicationsvc/jobmodel"
	"github.com/Angle-HR/server/internal/dbrouter"
	"github.com/Angle-HR/server/internal/hiring/appjobs"
	"github.com/Angle-HR/server/internal/hiring/hiringstore"
	"github.com/Angle-HR/server/internal/hiring/jobsource"
	"github.com/Angle-HR/server/internal/region"
)

// EnvJobSource selects where the service learns about jobs: "db" (default) or "events".
const EnvJobSource = "APPLICATIONS_JOBSOURCE"

// EnvJobFallback: with the events source, "db" (default) lets the read model ask the hiring tables for what it does
// not hold yet (read-through, an unindexed public id, a job without a recorded creator); "off" never reads them
// and answers not found instead. Every fallback use is counted (jobmodel.Stats).
const EnvJobFallback = "APPLICATIONS_JOBFALLBACK"

// Job source kinds.
const (
	JobSourceDB     = "db"
	JobSourceEvents = "events"
)

// JobSourceDeps is what a job source implementation may need.
type JobSourceDeps struct {
	Global *pgxpool.Pool
	Router *dbrouter.DBRouter
	// NATSURL is where an event-fed implementation subscribes. Empty when the bus is not configured.
	NATSURL string
	// Org, when set, answers TenantIDs (OrgTenants) and the notice recipient (OrgNotice) over gRPC.
	Org                   *OrgSource
	OrgTenants, OrgNotice bool
	// Stats counts fallback use of the events source; may be nil.
	Stats *jobmodel.Stats
	// NoFallback is APPLICATIONS_JOBFALLBACK=off.
	NoFallback bool
}

// NewJobSource picks the appjobs.JobSource implementation by kind.
func NewJobSource(kind string, d JobSourceDeps) (appjobs.JobSource, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", JobSourceDB:
		return NewDBJobSource(d), nil
	case JobSourceEvents:
		return NewEventsJobSource(d), nil
	default:
		return nil, fmt.Errorf("%s=%q: want %q or %q", EnvJobSource, kind, JobSourceDB, JobSourceEvents)
	}
}

// NewDBJobSource reads jobs from the hiring tables through internal/hiring/jobsource, as the monolith does. It is
// the default until the events read model exists.
func NewDBJobSource(d JobSourceDeps) appjobs.JobSource {
	return &jobsource.Source{
		Global: &hiringstore.Global{DB: d.Global},
		Pool: func(reg string) (hiringstore.DB, error) {
			p, err := d.Router.DB(region.Region(reg))
			if err != nil {
				return nil, err
			}
			return p, nil
		},
	}
}

// NewEventsJobSource serves jobs from the local read model (hiring.app_jobs, fed by hiring.job.* events through
// jobmodel.Projector). What events cannot answer, and jobs not yet projected, come from the DB adapter, which also
// fills the model on the way (read-through). See docs/microservices/applications-jobmodel.md.
func NewEventsJobSource(d JobSourceDeps) appjobs.JobSource {
	m := &jobmodel.Model{
		Pool: func(reg string) (jobmodel.DB, error) {
			p, err := d.Router.DB(region.Region(reg))
			if err != nil {
				return nil, err
			}
			return p, nil
		},
		Fallback:    NewDBJobSource(d),
		ReadThrough: true,
		Index:       &jobmodel.PGIndex{DB: d.Global},
		Stats:       d.Stats,
	}
	if d.NoFallback {
		m.Fallback, m.ReadThrough = nil, false
	}
	if d.Org != nil && d.OrgTenants {
		m.Tenants = d.Org.Tenants
	}
	if d.Org != nil && d.OrgNotice {
		m.Notice = d.Org.Notice
	}
	return m
}
