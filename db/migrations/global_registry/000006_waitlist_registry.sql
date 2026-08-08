-- +goose Up
-- +goose StatementBegin

CREATE TABLE waitlist.registry (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    waitlist_token UUID NOT NULL UNIQUE,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT waitlist_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia')),
    CONSTRAINT waitlist_registry_region_source_check
        CHECK (region_source IN ('explicit', 'inferred', 'jwt', 'subdomain', 'db', 'ip'))
);

CREATE INDEX waitlist_registry_token_idx ON waitlist.registry (waitlist_token);
CREATE INDEX waitlist_registry_region_idx ON waitlist.registry (region);

CREATE TRIGGER waitlist_registry_set_updated_at
    BEFORE UPDATE ON waitlist.registry
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO waitlist.registry (email, waitlist_token, region, region_source, created_at, updated_at)
SELECT email, waitlist_token, region, region_source, created_at, updated_at
FROM auth.users_registry
WHERE waitlist_token IS NOT NULL;

DELETE FROM auth.users_registry
WHERE waitlist_token IS NOT NULL
  AND user_id IS NULL;

DROP INDEX IF EXISTS auth.users_registry_waitlist_token_idx;

ALTER TABLE auth.users_registry
    DROP COLUMN waitlist_token;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE auth.users_registry
    ADD COLUMN waitlist_token UUID UNIQUE;

CREATE INDEX users_registry_waitlist_token_idx ON auth.users_registry (waitlist_token)
    WHERE waitlist_token IS NOT NULL;

-- Restore tokens onto existing product registry rows.
UPDATE auth.users_registry ur
SET waitlist_token = wr.waitlist_token
FROM waitlist.registry wr
WHERE ur.email = wr.email;

-- Re-insert waitlist-only rows that were removed during Up.
INSERT INTO auth.users_registry (email, waitlist_token, region, region_source, created_at, updated_at)
SELECT wr.email, wr.waitlist_token, wr.region, wr.region_source, wr.created_at, wr.updated_at
FROM waitlist.registry wr
WHERE NOT EXISTS (
    SELECT 1 FROM auth.users_registry ur WHERE ur.email = wr.email
);

DROP TRIGGER IF EXISTS waitlist_registry_set_updated_at ON waitlist.registry;
DROP TABLE IF EXISTS waitlist.registry;

-- +goose StatementEnd
