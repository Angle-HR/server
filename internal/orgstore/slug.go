package orgstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/Angle-HR/server/internal/region"
)

// ErrSlugTaken means another company already has the careers page address.
var ErrSlugTaken = errors.New("orgstore: slug taken")

// Slug limits. The database checks the same shape (accounts_organizations_slug_check, company_slugs_slug_check).
const (
	slugMin = 3
	slugMax = 40
)

// slugShape is lower-case letters and digits in words joined by single hyphens, none at either end.
var slugShape = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// reservedSlugs are addresses the product or the site uses, or that would pass for it. A company cannot take them.
var reservedSlugs = map[string]bool{
	"about": true, "admin": true, "api": true, "app": true, "auth": true, "blog": true, "careers": true,
	"companies": true, "company": true, "contact": true, "dashboard": true, "docs": true, "forgot-password": true,
	"help": true, "invite": true, "j": true, "jobs": true, "login": true, "logout": true, "onboarding": true,
	"open-hr": true, "openhr": true, "pricing": true, "privacy": true, "privacy-policy": true, "public": true,
	"reset-password": true, "security": true, "settings": true, "signup": true, "staging": true, "status": true,
	"support": true, "terms": true, "verify": true, "www": true,
}

// NormalizeSlug trims and lower-cases what the owner typed.
func NormalizeSlug(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidateSlug says why a normalized slug cannot be used, or "" when it can.
func ValidateSlug(s string) string {
	switch {
	case len(s) < slugMin:
		return fmt.Sprintf("use at least %d characters", slugMin)
	case len(s) > slugMax:
		return fmt.Sprintf("use at most %d characters", slugMax)
	case !slugShape.MatchString(s):
		return "use lower-case letters, numbers and single hyphens, with no hyphen at either end"
	case reservedSlugs[s]:
		return "this address is reserved"
	}
	return ""
}

// SuggestSlug turns a company name into a valid slug to offer the owner, or "" when nothing usable is left. It does
// not check availability.
func SuggestSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	s := b.String()
	if len(s) > slugMax {
		s = strings.TrimRight(s[:slugMax], "-")
	}
	if ValidateSlug(s) != "" {
		return ""
	}
	return s
}

// SlugRegistry is the global table of careers page addresses (org.company_slugs). Org is its only writer.
type SlugRegistry interface {
	// Claim gives the company the slug, replacing the one it had. ErrSlugTaken when another company has it.
	Claim(ctx context.Context, slug string, reg region.Region, companyID string) error
	// Release removes the company's slug, if it has one.
	Release(ctx context.Context, companyID string) error
	// Lookup finds the region and company of a slug. ErrNotFound for an unknown slug.
	Lookup(ctx context.Context, slug string) (region.Region, string, error)
	// Taken reports whether another company than companyID has the slug.
	Taken(ctx context.Context, slug, companyID string) (bool, error)
}

// GlobalSlugs implements SlugRegistry on the global database.
type GlobalSlugs struct{ DB DB }

var _ SlugRegistry = GlobalSlugs{}

const claimSlugSQL = `
INSERT INTO org.company_slugs (slug, region, organization_id) VALUES ($1, $2, $3::uuid)
ON CONFLICT (organization_id) DO UPDATE SET slug = EXCLUDED.slug, region = EXCLUDED.region, updated_at = now()`

// Claim implements SlugRegistry. The primary key refuses a slug that belongs to another company; the conflict on
// organization_id moves the company to the new slug, which frees the old one.
func (g GlobalSlugs) Claim(ctx context.Context, slug string, reg region.Region, companyID string) error {
	_, err := g.DB.Exec(ctx, claimSlugSQL, slug, string(reg), companyID)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrSlugTaken
	}
	if err != nil {
		return fmt.Errorf("orgstore: claim slug: %w", err)
	}
	return nil
}

// Release implements SlugRegistry.
func (g GlobalSlugs) Release(ctx context.Context, companyID string) error {
	if _, err := g.DB.Exec(ctx, `DELETE FROM org.company_slugs WHERE organization_id = $1::uuid`, companyID); err != nil {
		return fmt.Errorf("orgstore: release slug: %w", err)
	}
	return nil
}

// Lookup implements SlugRegistry.
func (g GlobalSlugs) Lookup(ctx context.Context, slug string) (region.Region, string, error) {
	var reg, id string
	err := g.DB.QueryRow(ctx, `SELECT region, organization_id::text FROM org.company_slugs WHERE slug = $1`, slug).Scan(&reg, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("orgstore: lookup slug: %w", err)
	}
	return region.Region(reg), id, nil
}

// Taken implements SlugRegistry.
func (g GlobalSlugs) Taken(ctx context.Context, slug, companyID string) (bool, error) {
	var taken bool
	err := g.DB.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM org.company_slugs WHERE slug = $1 AND organization_id <> $2::uuid)`, slug, companyID).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("orgstore: slug taken: %w", err)
	}
	return taken, nil
}
