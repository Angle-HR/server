CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users_registry (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu')),
    CONSTRAINT users_registry_region_source_check
        CHECK (region_source IN ('explicit', 'inferred', 'jwt', 'subdomain', 'db', 'ip'))
);

CREATE INDEX users_registry_region_idx ON users_registry (region);

CREATE TRIGGER users_registry_set_updated_at
    BEFORE UPDATE ON users_registry
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE countries (
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
        CHECK (region IN ('uk', 'us', 'africa', 'eu'))
);

CREATE INDEX countries_region_idx ON countries (region);
CREATE INDEX countries_active_sort_idx ON countries (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER countries_set_updated_at
    BEFORE UPDATE ON countries
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO countries (id, name, slug, region, icon_key, sort_order) VALUES
    ('a1b2c3d4-e5f6-4789-a012-3456789abcde', 'United Kingdom', 'united-kingdom', 'uk', 'flag-uk', 1),
    ('b2c3d4e5-f6a7-4890-b123-456789abcdef', 'European Union', 'european-union', 'eu', 'flag-eu', 2),
    ('c3d4e5f6-a7b8-4901-c234-56789abcdef0', 'United States', 'united-states', 'us', 'flag-us', 3),
    ('d4e5f6a7-b8c9-4012-d345-6789abcdef01', 'Nigeria', 'nigeria', 'africa', 'flag-ng', 4),
    ('e5f6a7b8-c9d0-4123-e456-789abcdef012', 'Kenya', 'kenya', 'africa', 'flag-ke', 5),
    ('f6a7b8c9-d0e1-4234-f567-89abcdef0123', 'South Africa', 'south-africa', 'africa', 'flag-za', 6);

CREATE TABLE tenant_subdomains (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subdomain TEXT NOT NULL UNIQUE,
    region TEXT NOT NULL,
    tenant_id UUID NOT NULL,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenant_subdomains_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'))
);

CREATE INDEX tenant_subdomains_subdomain_idx ON tenant_subdomains (subdomain);
