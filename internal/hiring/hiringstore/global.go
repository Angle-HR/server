package hiringstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Angle-HR/server/internal/hiring/gates"
	"github.com/Angle-HR/server/internal/hiring/hiringtypes"
	"github.com/Angle-HR/server/internal/hiring/jobs"
)

// Global reads the reference data that is the same for every company: markets and their warnings, seniority
// levels, experience ranges, skills and industries. It holds no personal data, so it uses the global database
// without a tenant.
type Global struct{ DB DB }

// Shared types, aliased so the store's API reads in terms of them.
type (
	CatalogItem = hiringtypes.CatalogItem
	RefCheck    = hiringtypes.RefCheck
)

const marketsSQL = `
SELECT code, name, is_open, coalesce(min_retention_months, 0), selection_warnings
FROM hiring.markets ORDER BY sort_order, code`

// Markets returns every market, open or not, with the warnings to show when it is picked.
func (g *Global) Markets(ctx context.Context) (jobs.MarketCatalog, []jobs.Market, error) {
	rows, err := g.DB.Query(ctx, marketsSQL)
	if err != nil {
		return nil, nil, fmt.Errorf("hiringstore: markets: %w", err)
	}
	defer rows.Close()
	cat := jobs.MarketCatalog{}
	var list []jobs.Market
	for rows.Next() {
		var m jobs.Market
		var raw []byte
		if scanErr := rows.Scan(&m.Code, &m.Name, &m.IsOpen, &m.MinRetentionMonths, &raw); scanErr != nil {
			return nil, nil, scanErr
		}
		m.Warnings = []jobs.Warning{}
		if len(raw) > 0 {
			if jsonErr := json.Unmarshal(raw, &m.Warnings); jsonErr != nil {
				return nil, nil, fmt.Errorf("hiringstore: market %s warnings: %w", m.Code, jsonErr)
			}
		}
		for i := range m.Warnings {
			m.Warnings[i].Market = m.Code
		}
		cat[m.Code] = m
		list = append(list, m)
	}
	return cat, list, rows.Err()
}

const seniorityLevelsSQL = `
SELECT id::text, slug, name FROM hiring.seniority_levels WHERE is_active ORDER BY sort_order, slug`

const experienceRangesSQL = `
SELECT id::text, slug, name FROM hiring.experience_ranges WHERE is_active ORDER BY sort_order, slug`

const industriesSQL = `
SELECT id::text, slug, name FROM accounts.onboarding_industries WHERE is_active ORDER BY sort_order, name`

func (g *Global) items(ctx context.Context, sql string, args ...any) ([]CatalogItem, error) {
	rows, err := g.DB.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: catalog: %w", err)
	}
	defer rows.Close()
	out := []CatalogItem{}
	for rows.Next() {
		var it CatalogItem
		if err := rows.Scan(&it.ID, &it.Slug, &it.Name); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// SeniorityLevels lists the active seniority levels.
func (g *Global) SeniorityLevels(ctx context.Context) ([]CatalogItem, error) {
	return g.items(ctx, seniorityLevelsSQL)
}

// ExperienceRanges lists the active experience ranges.
func (g *Global) ExperienceRanges(ctx context.Context) ([]CatalogItem, error) {
	return g.items(ctx, experienceRangesSQL)
}

// Industries lists the active industries.
func (g *Global) Industries(ctx context.Context) ([]CatalogItem, error) {
	return g.items(ctx, industriesSQL)
}

const searchSkillsSQL = `
SELECT id::text, slug, name FROM hiring.skills
WHERE is_active AND lower(name) LIKE $1::text ESCAPE '\'
ORDER BY lower(name), id LIMIT $2::int`

// Bounds of a skill search.
const (
	defaultSkillResults = 20
	maxSkillResults     = 50
)

// SearchSkills finds skills whose name starts with q (case-insensitive). An empty q lists the first few.
func (g *Global) SearchSkills(ctx context.Context, q string, limit int) ([]CatalogItem, error) {
	if limit < 1 || limit > maxSkillResults {
		limit = defaultSkillResults
	}
	return g.items(ctx, searchSkillsSQL, EscapeLike(strings.ToLower(strings.TrimSpace(q)))+"%", limit)
}

const existingIDsSQL = `
SELECT
  coalesce((SELECT array_agg(s.id::text) FROM hiring.skills s WHERE s.id = ANY($1::uuid[]) AND s.is_active), '{}'),
  EXISTS (SELECT 1 FROM accounts.onboarding_industries
          WHERE id = nullif($2::text, '')::uuid AND is_active) OR $2::text = '',
  EXISTS (SELECT 1 FROM hiring.seniority_levels WHERE id = nullif($3::text, '')::uuid AND is_active) OR $3::text = '',
  EXISTS (SELECT 1 FROM hiring.experience_ranges WHERE id = nullif($4::text, '')::uuid AND is_active) OR $4::text = ''`

const skillNamesSQL = `SELECT id::text, name FROM hiring.skills WHERE id = ANY($1::uuid[])`

// CheckRefs looks up the skill, industry, seniority and experience ids a draft refers to. The regional database
// cannot enforce these with foreign keys because the catalogs live in the global database.
func (g *Global) CheckRefs(
	ctx context.Context, skillIDs []string, industry, seniority, experience string,
) (*RefCheck, error) {
	if skillIDs == nil {
		skillIDs = []string{}
	}
	var found []string
	rc := &RefCheck{}
	err := g.DB.QueryRow(ctx, existingIDsSQL, skillIDs, industry, seniority, experience).
		Scan(&found, &rc.IndustryOK, &rc.SeniorityOK, &rc.ExperienceOK)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: check refs: %w", err)
	}
	have := map[string]bool{}
	for _, id := range found {
		have[id] = true
	}
	for _, id := range skillIDs {
		if !have[id] {
			rc.UnknownSkills = append(rc.UnknownSkills, id)
		}
	}
	return rc, nil
}

// SkillNames resolves skill ids to display names.
func (g *Global) SkillNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := g.DB.Query(ctx, skillNamesSQL, ids)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: skill names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// ---- compliance gates and the public registry (phase 2) ----

const gatesSQL = `
SELECT id, COALESCE(market_code, ''), platform, severity, requirement, COALESCE(legal_basis, ''), trigger, version
FROM hiring.compliance_gates WHERE is_active ORDER BY market_code NULLS LAST, id`

// Gates returns the active compliance gate catalog.
func (g *Global) Gates(ctx context.Context) ([]gates.Gate, error) {
	rows, err := g.DB.Query(ctx, gatesSQL)
	if err != nil {
		return nil, fmt.Errorf("hiringstore: gates: %w", err)
	}
	defer rows.Close()
	out := []gates.Gate{}
	for rows.Next() {
		var gt gates.Gate
		var trigger []byte
		if err = rows.Scan(&gt.ID, &gt.MarketCode, &gt.Platform, &gt.Severity, &gt.Requirement, &gt.LegalBasis,
			&trigger, &gt.Version); err != nil {
			return nil, fmt.Errorf("hiringstore: scan gate: %w", err)
		}
		gt.Trigger = json.RawMessage(trigger)
		out = append(out, gt)
	}
	return out, rows.Err()
}

const registerJobSQL = `
INSERT INTO hiring.job_registry (public_id, region, organization_id, status, published_at, valid_through)
VALUES ($1, $2, $3::uuid, $4, $5, NULLIF($6, '')::date)
ON CONFLICT (public_id) DO UPDATE SET
    status = EXCLUDED.status, valid_through = EXCLUDED.valid_through,
    published_at = COALESCE(hiring.job_registry.published_at, EXCLUDED.published_at)
WHERE hiring.job_registry.organization_id = EXCLUDED.organization_id AND hiring.job_registry.region = EXCLUDED.region`

// RegisterJob records which region and company hold a published job. Registering the same job again updates its
// status and closing date; a public id that belongs to another company or region is never overwritten.
func (g *Global) RegisterJob(ctx context.Context, e hiringtypes.RegistryEntry) error {
	tag, err := g.DB.Exec(ctx, registerJobSQL, e.PublicID, e.Region, e.OrganizationID, e.Status, e.PublishedAt, e.ValidThrough)
	if err != nil {
		return fmt.Errorf("hiringstore: register job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("hiringstore: register job: public id %s belongs to another company", e.PublicID)
	}
	return nil
}

const setRegistryStatusSQL = `UPDATE hiring.job_registry SET status = $2 WHERE public_id = $1`

// SetRegistryStatus follows a status change of a registered job.
func (g *Global) SetRegistryStatus(ctx context.Context, publicID, status string) error {
	tag, err := g.DB.Exec(ctx, setRegistryStatusSQL, publicID, status)
	if err != nil {
		return fmt.Errorf("hiringstore: registry status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
