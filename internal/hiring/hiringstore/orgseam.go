package hiringstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CompanyOrg is where a Store gets what org owns about a company when ORG_COMPANY=grpc: the row the public page and
// the job events carry, the publish prerequisites of the settings page, and the two writes (accept the DPA, set the
// privacy contact). With a nil CompanyOrg the Store reads and writes accounts.* itself (the default). The gRPC
// implementation is orgclient.GRPCCompany. Every call carries the region of the Store.
type CompanyOrg interface {
	// Profile is the legal name, privacy contact and DPO of a company. ErrNotFound for an unknown company.
	Profile(ctx context.Context, region, tenantID string) (CompanyProfile, error)
	// Setup is the company's KYB status, DPA acceptance and privacy contact.
	Setup(ctx context.Context, region, tenantID, dpaVersion string) (CompanyReadiness, error)
	AcceptAgreement(ctx context.Context, region, tenantID, userID, version string) error
	SetPrivacyContact(ctx context.Context, region, tenantID, email, dpo string) error
}

// CompanyProfile is what the company told candidates about itself.
type CompanyProfile struct {
	// Name is the legal name. DisplayName, when set, is what the careers page shows instead.
	Name, PrivacyEmail, DPOContact string
	// The public profile: the careers page address, display name, about text, website and logo.
	Slug, DisplayName, About, Website, LogoURL string
}

// CompanyReadiness is the company part of CompanySetup.
type CompanyReadiness struct {
	KYBStatus     string
	DPAAcceptedAt *time.Time
	PrivacyEmail  string
	DPOContact    string
}

// UserNames resolves display names of users (IDENTITY_USERS=grpc: IdentityService.BatchGetUsers). Inactive users
// are absent from the result, as the accounts.users join filtered them.
type UserNames interface {
	Names(ctx context.Context, region string, userIDs []string) (map[string]string, error)
}

// companyProfile reads the company row the public page and the events carry: from org when configured, else
// from accounts.organizations through tx.
func (s *Store) companyProfile(ctx context.Context, tx pgx.Tx, tenantID string) (CompanyProfile, error) {
	if s.Org != nil {
		return s.Org.Profile(ctx, s.Region, tenantID)
	}
	var p CompanyProfile
	if err := tx.QueryRow(ctx, publicCompanyFullSQL, tenantID).Scan(&p.Name, &p.PrivacyEmail, &p.DPOContact,
		&p.Slug, &p.DisplayName, &p.About, &p.Website, &p.LogoURL); err != nil {
		return p, fmt.Errorf("hiringstore: load company: %w", err)
	}
	return p, nil
}
