-- Org: the company's public profile, shown on its careers page (/company/<slug>). The owner chooses the slug.
-- The slug is unique across every region through org.company_slugs in the global database (global_registry 000021);
-- the unique index here only guards one region. Name, about, website and logo are what the company tells the public
-- about itself; display_name falls back to legal_name when empty.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE accounts.organizations
    ADD COLUMN IF NOT EXISTS slug TEXT,
    ADD COLUMN IF NOT EXISTS display_name TEXT,
    ADD COLUMN IF NOT EXISTS about TEXT,
    ADD COLUMN IF NOT EXISTS website TEXT,
    ADD COLUMN IF NOT EXISTS logo_url TEXT;

ALTER TABLE accounts.organizations
    ADD CONSTRAINT accounts_organizations_slug_check
        CHECK (slug IS NULL OR slug ~ '^[a-z0-9](?:[a-z0-9]|-(?=[a-z0-9])){1,38}[a-z0-9]$'),
    ADD CONSTRAINT accounts_organizations_display_name_check
        CHECK (display_name IS NULL OR char_length(display_name) BETWEEN 1 AND 100),
    ADD CONSTRAINT accounts_organizations_about_check
        CHECK (about IS NULL OR char_length(about) <= 1500),
    ADD CONSTRAINT accounts_organizations_website_check
        CHECK (website IS NULL OR (char_length(website) <= 300 AND website ~ '^https://')),
    ADD CONSTRAINT accounts_organizations_logo_url_check
        CHECK (logo_url IS NULL OR (char_length(logo_url) <= 500 AND logo_url ~ '^https://'));

CREATE UNIQUE INDEX IF NOT EXISTS accounts_organizations_slug_idx
    ON accounts.organizations (slug) WHERE slug IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS accounts.accounts_organizations_slug_idx;
ALTER TABLE accounts.organizations
    DROP CONSTRAINT IF EXISTS accounts_organizations_logo_url_check,
    DROP CONSTRAINT IF EXISTS accounts_organizations_website_check,
    DROP CONSTRAINT IF EXISTS accounts_organizations_about_check,
    DROP CONSTRAINT IF EXISTS accounts_organizations_display_name_check,
    DROP CONSTRAINT IF EXISTS accounts_organizations_slug_check,
    DROP COLUMN IF EXISTS logo_url,
    DROP COLUMN IF EXISTS website,
    DROP COLUMN IF EXISTS about,
    DROP COLUMN IF EXISTS display_name,
    DROP COLUMN IF EXISTS slug;
-- +goose StatementEnd
