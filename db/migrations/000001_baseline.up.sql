CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION soft_delete_row()
RETURNS TRIGGER AS $$
BEGIN
    IF OLD.deleted_at IS NULL THEN
        EXECUTE format(
            'UPDATE %I.%I SET deleted_at = now(), updated_at = now() WHERE id = $1',
            TG_TABLE_SCHEMA,
            TG_TABLE_NAME
        ) USING OLD.id;
    END IF;

    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE SCHEMA IF NOT EXISTS waitlist;

CREATE TABLE waitlist.waitlist (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    full_name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    country_id UUID NOT NULL,
    region TEXT NOT NULL,
    region_source TEXT NOT NULL,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'))
);

CREATE INDEX waitlist_country_id_idx ON waitlist.waitlist (country_id);
CREATE INDEX waitlist_region_idx ON waitlist.waitlist (region);

CREATE TRIGGER waitlist_set_updated_at
    BEFORE UPDATE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_soft_delete
    BEFORE DELETE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION soft_delete_row();
