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

CREATE TABLE waitlist (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    company TEXT,
    full_name TEXT,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    ip_address INET,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu')),
    CONSTRAINT waitlist_region_source_check
        CHECK (region_source IN ('explicit', 'inferred'))
);

CREATE INDEX waitlist_email_idx ON waitlist (email);

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
