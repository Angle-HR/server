package applicationsvc

import (
	"context"

	"github.com/Angle-HR/server/internal/hiring/appstore"
)

// StoreLookup answers what the gRPC API needs beyond the application services: which job an application is on,
// and the per-job applicant counters. It reads the applications tables of one region.
type StoreLookup struct {
	// Pool returns a region's database.
	Pool func(reg string) (appstore.DB, error)
}

func (l *StoreLookup) store(reg string) (*appstore.Store, error) {
	p, err := l.Pool(reg)
	if err != nil {
		return nil, err
	}
	return &appstore.Store{DB: p, Region: reg}, nil
}

// JobOfApplication returns the job id of an application of the company.
func (l *StoreLookup) JobOfApplication(ctx context.Context, reg, tenantID, applicationID string) (string, error) {
	st, err := l.store(reg)
	if err != nil {
		return "", err
	}
	return st.JobOfApplication(ctx, tenantID, applicationID)
}

// ApplicantCounts returns the counters of the given jobs of the company.
func (l *StoreLookup) ApplicantCounts(ctx context.Context, reg, tenantID string, jobIDs []string) ([]appstore.ApplicantCount, error) {
	st, err := l.store(reg)
	if err != nil {
		return nil, err
	}
	return st.ApplicantCounts(ctx, tenantID, jobIDs)
}

// Lookup is what the gRPC server needs from StoreLookup, so tests can fake it.
type Lookup interface {
	JobOfApplication(ctx context.Context, reg, tenantID, applicationID string) (string, error)
	ApplicantCounts(ctx context.Context, reg, tenantID string, jobIDs []string) ([]appstore.ApplicantCount, error)
}

var _ Lookup = (*StoreLookup)(nil)
