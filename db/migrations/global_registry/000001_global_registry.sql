-- +goose Up
-- +goose StatementBegin
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS auth;
CREATE SCHEMA IF NOT EXISTS waitlist;
CREATE SCHEMA IF NOT EXISTS accounts;
CREATE SCHEMA IF NOT EXISTS fluvio;

CREATE TABLE auth.users_registry (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    waitlist_token UUID UNIQUE,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT users_registry_region_source_check
        CHECK (region_source IN ('explicit', 'inferred', 'jwt', 'subdomain', 'db', 'ip'))
);

CREATE INDEX users_registry_region_idx ON auth.users_registry (region);

CREATE TRIGGER users_registry_set_updated_at
    BEFORE UPDATE ON auth.users_registry
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE waitlist.countries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    region TEXT NOT NULL,
    icon_key TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT countries_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'))
);

CREATE INDEX countries_region_idx ON waitlist.countries (region);
CREATE INDEX countries_active_sort_idx ON waitlist.countries (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER countries_set_updated_at
    BEFORE UPDATE ON waitlist.countries
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO waitlist.countries (id, name, slug, region, icon_key, sort_order) VALUES
    ('b2c3d4e5-f6a7-4890-b123-456789abcdef', 'European Union', 'european-union', 'eu', 'flag-eu', 1),
    ('a7b8c9d0-e1f2-4345-a678-9abcdef01234', 'Germany', 'germany', 'eu', 'flag-de', 2),
    ('b8c9d0e1-f2a3-4456-b789-abcdef012345', 'India', 'india', 'asia', 'flag-in', 3),
    ('e5f6a7b8-c9d0-4123-e456-789abcdef012', 'Kenya', 'kenya', 'africa', 'flag-ke', 4),
    ('d4e5f6a7-b8c9-4012-d345-6789abcdef01', 'Nigeria', 'nigeria', 'africa', 'flag-ng', 5),
<<<<<<< HEAD:db/migrations/global_registry/000001_global_registry.up.sql
    ('f6a7b8c9-d0e1-4234-f567-89abcdef0123', 'South Africa', 'south-africa', 'africa', 'flag-za', 6)
=======
    ('f6a7b8c9-d0e1-4234-f567-89abcdef0123', 'South Africa', 'south-africa', 'africa', 'flag-za', 6),
>>>>>>> 01c841e4f63cc434b516f62c4f4cfe6ade14aad1:db/migrations/global_registry/000001_global_registry.sql
    ('a1b2c3d4-e5f6-4789-a012-3456789abcde', 'United Kingdom', 'united-kingdom', 'uk', 'flag-uk', 7),
    ('c3d4e5f6-a7b8-4901-c234-56789abcdef0', 'United States', 'united-states', 'us', 'flag-us', 8);

UPDATE waitlist.countries SET is_active = FALSE WHERE slug = 'south-africa';

CREATE TABLE auth.tenant_subdomains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subdomain TEXT NOT NULL UNIQUE,
    region TEXT NOT NULL,
    tenant_id UUID NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenant_subdomains_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'))
);

CREATE INDEX tenant_subdomains_subdomain_idx ON auth.tenant_subdomains (subdomain);

CREATE INDEX users_registry_waitlist_token_idx ON auth.users_registry (waitlist_token)
    WHERE waitlist_token IS NOT NULL;

CREATE TABLE waitlist.industries (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    emoji TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX industries_active_sort_idx ON waitlist.industries (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER industries_set_updated_at
    BEFORE UPDATE ON waitlist.industries
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE waitlist.hiring_tools (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    icon_url TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX hiring_tools_active_sort_idx ON waitlist.hiring_tools (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER hiring_tools_set_updated_at
    BEFORE UPDATE ON waitlist.hiring_tools
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE waitlist.hiring_frustrations (
    id UUID PRIMARY KEY,
    description TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    emoji TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX hiring_frustrations_active_sort_idx ON waitlist.hiring_frustrations (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER hiring_frustrations_set_updated_at
    BEFORE UPDATE ON waitlist.hiring_frustrations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE waitlist.roles (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    emoji TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX roles_active_sort_idx ON waitlist.roles (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER roles_set_updated_at
    BEFORE UPDATE ON waitlist.roles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE waitlist.team_sizes (
    id UUID PRIMARY KEY,
    label TEXT NOT NULL,
    min_size INTEGER,
    max_size INTEGER,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT team_sizes_range_check CHECK (
        min_size IS NULL OR max_size IS NULL OR min_size <= max_size
    )
);

CREATE INDEX team_sizes_sort_idx ON waitlist.team_sizes (sort_order);

CREATE TRIGGER team_sizes_set_updated_at
    BEFORE UPDATE ON waitlist.team_sizes
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO waitlist.industries (id, name, slug, emoji, sort_order) VALUES
    ('10000000-0000-4000-8000-000000000001', 'Tech', 'tech', E'⌨️', 1),
    ('10000000-0000-4000-8000-000000000002', 'Energy', 'energy', E'♻️', 2),
    ('10000000-0000-4000-8000-000000000003', 'Green', 'green', E'🌴', 3),
    ('10000000-0000-4000-8000-000000000004', 'Fintech', 'fintech', E'💸', 4),
    ('10000000-0000-4000-8000-000000000005', 'Health', 'health', E'💪', 5),
    ('10000000-0000-4000-8000-000000000006', 'Education', 'education', E'📚', 6),
    ('10000000-0000-4000-8000-000000000007', 'Security', 'security', E'🔒', 7),
    ('10000000-0000-4000-8000-000000000008', 'Construction', 'construction', E'🏗️', 8),
    ('10000000-0000-4000-8000-000000000009', 'Hardware', 'hardware', E'🛠️', 9),
    ('10000000-0000-4000-8000-00000000000a', 'Others', 'others', NULL, 10);

INSERT INTO waitlist.hiring_tools (id, name, slug, icon_url, sort_order) VALUES
    ('20000000-0000-4000-8000-000000000001', 'Notion', 'notion', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 1),
    ('20000000-0000-4000-8000-000000000002', 'Google Forms', 'google-forms', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 2),
    ('20000000-0000-4000-8000-000000000003', 'Excel', 'excel', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 3),
    ('20000000-0000-4000-8000-000000000004', 'Slack', 'slack', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 4),
    ('20000000-0000-4000-8000-000000000005', 'Google Docs', 'google-docs', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 5),
    ('20000000-0000-4000-8000-000000000006', 'Airtable', 'airtable', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 6),
    ('20000000-0000-4000-8000-000000000007', 'Calendly', 'calendly', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 7),
    ('20000000-0000-4000-8000-000000000008', 'Typeform', 'typeform', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 8),
    ('20000000-0000-4000-8000-000000000009', 'Gmail', 'gmail', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 9),
    ('20000000-0000-4000-8000-00000000000a', 'DocuSign', 'docusign', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 10),
    ('20000000-0000-4000-8000-00000000000b', 'Zoom', 'zoom', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 11),
    ('20000000-0000-4000-8000-00000000000c', 'Other ATS tools', 'other-ats-tools', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 12),
    ('20000000-0000-4000-8000-00000000000d', 'Others', 'others', 'https://cdn.example.com/waitlist/icons/placeholder.svg', 13);

INSERT INTO waitlist.hiring_frustrations (id, description, slug, emoji, sort_order) VALUES
    ('30000000-0000-4000-8000-000000000001', 'Finding the right candidates / Not knowing where to post', 'finding-candidates', E'🔍', 1),
    ('30000000-0000-4000-8000-000000000002', 'Tracking and Managing candidates across different tools', 'tracking-candidates', E'📋', 2),
    ('30000000-0000-4000-8000-000000000003', 'No clear hiring pipeline or stages', 'no-pipeline', E'🔄', 3),
    ('30000000-0000-4000-8000-000000000004', 'Following up with candidates manually', 'manual-follow-up', E'✉️', 4),
    ('30000000-0000-4000-8000-000000000005', 'No easy way to collect team feedback on candidates', 'team-feedback', E'💬', 5),
    ('30000000-0000-4000-8000-000000000006', 'Manual onboarding and off-boarding process', 'manual-onboarding', E'📦', 6),
    ('30000000-0000-4000-8000-000000000007', 'Others', 'others', NULL, 7);

INSERT INTO waitlist.roles (id, name, slug, emoji, sort_order) VALUES
    ('40000000-0000-4000-8000-000000000001', 'Founder', 'founder', E'🤴', 1),
    ('40000000-0000-4000-8000-000000000002', 'HR / People', 'hr-people', E'👥', 2),
    ('40000000-0000-4000-8000-000000000003', 'Engineer', 'engineer', E'👨‍💻', 3),
    ('40000000-0000-4000-8000-000000000004', 'Designer', 'designer', E'🎨', 4),
    ('40000000-0000-4000-8000-000000000005', 'Marketing', 'marketing', E'📣', 5),
    ('40000000-0000-4000-8000-000000000006', 'Operations', 'operations', E'⚙️', 6),
    ('40000000-0000-4000-8000-000000000007', 'Others', 'others', NULL, 7);

INSERT INTO waitlist.team_sizes (id, label, min_size, max_size, sort_order) VALUES
    ('50000000-0000-4000-8000-000000000001', 'Just me', 1, 1, 1),
    ('50000000-0000-4000-8000-000000000002', '2-10', 2, 10, 2),
    ('50000000-0000-4000-8000-000000000003', '10-20', 10, 20, 3),
    ('50000000-0000-4000-8000-000000000004', '20+', 21, NULL, 4);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS team_sizes_set_updated_at ON waitlist.team_sizes;
DROP TRIGGER IF EXISTS roles_set_updated_at ON waitlist.roles;
DROP TRIGGER IF EXISTS hiring_frustrations_set_updated_at ON waitlist.hiring_frustrations;
DROP TRIGGER IF EXISTS hiring_tools_set_updated_at ON waitlist.hiring_tools;
DROP TRIGGER IF EXISTS industries_set_updated_at ON waitlist.industries;
DROP TRIGGER IF EXISTS countries_set_updated_at ON waitlist.countries;
DROP TRIGGER IF EXISTS users_registry_set_updated_at ON auth.users_registry;

DROP TABLE IF EXISTS waitlist.team_sizes;
DROP TABLE IF EXISTS waitlist.roles;
DROP TABLE IF EXISTS waitlist.hiring_frustrations;
DROP TABLE IF EXISTS waitlist.hiring_tools;
DROP TABLE IF EXISTS waitlist.industries;
DROP TABLE IF EXISTS auth.tenant_subdomains;
DROP TABLE IF EXISTS waitlist.countries;
DROP TABLE IF EXISTS auth.users_registry;

DROP SCHEMA IF EXISTS fluvio;
DROP SCHEMA IF EXISTS accounts;
DROP SCHEMA IF EXISTS waitlist;
DROP SCHEMA IF EXISTS auth;

DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
-- +goose StatementEnd
