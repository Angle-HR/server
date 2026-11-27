package orgstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/orgevents"
	"github.com/Angle-HR/server/internal/region"
)

// Profile is what a company tells the public about itself. Empty strings mean "not set".
type Profile struct {
	Slug        string
	DisplayName string
	About       string
	Website     string
	LogoURL     string
	// LegalName is shown when DisplayName is empty.
	LegalName string
}

// Name is the name the careers page shows.
func (p Profile) Name() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.LegalName
}

const profileSQL = `
SELECT coalesce(slug, ''), coalesce(display_name, ''), coalesce(about, ''), coalesce(website, ''),
       coalesce(logo_url, ''), legal_name
FROM accounts.organizations WHERE id = $1::uuid`

// Profile loads the company's public profile.
func (s *Store) Profile(ctx context.Context, reg region.Region, companyID string) (*Profile, error) {
	d, err := s.db(reg)
	if err != nil {
		return nil, err
	}
	var p Profile
	err = d.QueryRow(ctx, profileSQL, companyID).Scan(&p.Slug, &p.DisplayName, &p.About, &p.Website, &p.LogoURL, &p.LegalName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("orgstore: profile: %w", err)
	}
	return &p, nil
}

const setProfileSQL = `
UPDATE accounts.organizations
SET slug = NULLIF($2, ''), display_name = NULLIF($3, ''), about = NULLIF($4, ''), website = NULLIF($5, ''),
    logo_url = NULLIF($6, '')
WHERE id = $1::uuid`

// SetProfile replaces the company's public profile. The slug is claimed in the global registry first, so two
// companies in different regions cannot take the same one; the regional row and the org.company.profile_updated.v1
// event (when ORG_EVENTS=on) are written in one transaction after that. The caller has validated every field.
//
// If the regional write fails after the claim, the claim is rolled back to the slug the company had, so its
// careers page keeps working (the page is served only when the registry and the regional row agree).
func (s *Store) SetProfile(ctx context.Context, reg region.Region, companyID string, p Profile) (err error) {
	d, err := s.db(reg)
	if err != nil {
		return err
	}
	if p.Slug != "" {
		if s.Slugs == nil {
			return fmt.Errorf("orgstore: no slug registry configured")
		}
		var prev string
		if err = d.QueryRow(ctx, `SELECT coalesce(slug, '') FROM accounts.organizations WHERE id = $1::uuid`, companyID).Scan(&prev); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("orgstore: current slug: %w", err)
		}
		if err = s.Slugs.Claim(ctx, p.Slug, reg, companyID); err != nil {
			return err
		}
		if prev != p.Slug {
			defer func() {
				if err == nil {
					return
				}
				// Best effort, and not tied to a request that may already be cancelled.
				bg := context.WithoutCancel(ctx)
				if prev != "" {
					_ = s.Slugs.Claim(bg, prev, reg, companyID)
				} else {
					_ = s.Slugs.Release(bg, companyID)
				}
			}()
		}
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("orgstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, setProfileSQL, companyID, p.Slug, strings.TrimSpace(p.DisplayName), strings.TrimSpace(p.About),
		strings.TrimSpace(p.Website), strings.TrimSpace(p.LogoURL))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("orgstore: set profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	kind, err := orgevents.KindOf(ctx, tx, companyID)
	if err != nil {
		return err
	}
	if err = orgevents.EnqueueProfileUpdated(ctx, tx, orgevents.CompanyProfileUpdated{
		CompanyKind: kind, CompanyID: companyID, Region: string(reg),
		Changed: []string{"public_profile"}, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("orgstore: commit: %w", err)
	}
	return nil
}

// SlugAvailable reports whether the company can take the slug: valid, not reserved, and not another company's.
// The reason is "" when it can.
func (s *Store) SlugAvailable(ctx context.Context, companyID, slug string) (bool, string, error) {
	slug = NormalizeSlug(slug)
	if why := ValidateSlug(slug); why != "" {
		return false, why, nil
	}
	if s.Slugs == nil {
		return false, "", fmt.Errorf("orgstore: no slug registry configured")
	}
	taken, err := s.Slugs.Taken(ctx, slug, companyID)
	if err != nil {
		return false, "", err
	}
	if taken {
		return false, "this address is already used by another company", nil
	}
	return true, "", nil
}
