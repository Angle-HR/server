-- Careers page addresses. The public URL /company/<slug> carries no region, so the slug is looked up here to find the
-- region and company, then read from that region's database. Org owns the table and is the only writer; hiring reads
-- it to list a company's open roles. A company has at most one slug, and a slug belongs to one company.

-- +goose Up
-- +goose StatementBegin
CREATE SCHEMA IF NOT EXISTS org;

CREATE TABLE org.company_slugs (
    slug TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    organization_id UUID NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT company_slugs_region_check CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT company_slugs_slug_check
        CHECK (slug ~ '^[a-z0-9](?:[a-z0-9]|-(?=[a-z0-9])){1,38}[a-z0-9]$')
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS org.company_slugs;
DROP SCHEMA IF EXISTS org;
-- +goose StatementEnd
