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
    onboarding_submitted_at TIMESTAMPTZ,
    wants_early_access BOOLEAN,
    wants_user_testing BOOLEAN,
    role_id UUID,
    team_size_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'))
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

CREATE TABLE waitlist.waitlist_industries (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    industry_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, industry_id)
);

CREATE INDEX waitlist_industries_waitlist_id_idx ON waitlist.waitlist_industries (waitlist_id);

CREATE TABLE waitlist.waitlist_hiring_tools (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    hiring_tool_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, hiring_tool_id)
);

CREATE INDEX waitlist_hiring_tools_waitlist_id_idx ON waitlist.waitlist_hiring_tools (waitlist_id);

CREATE TABLE waitlist.waitlist_frustrations (
    waitlist_id BIGINT NOT NULL REFERENCES waitlist.waitlist (id) ON DELETE CASCADE,
    frustration_id UUID NOT NULL,
    other_text TEXT,
    PRIMARY KEY (waitlist_id, frustration_id)
);

CREATE INDEX waitlist_frustrations_waitlist_id_idx ON waitlist.waitlist_frustrations (waitlist_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS waitlist_soft_delete ON waitlist.waitlist;
DROP TRIGGER IF EXISTS waitlist_set_updated_at ON waitlist.waitlist;
DROP TABLE IF EXISTS waitlist.waitlist_frustrations;
DROP TABLE IF EXISTS waitlist.waitlist_hiring_tools;
DROP TABLE IF EXISTS waitlist.waitlist_industries;
DROP TABLE IF EXISTS waitlist.waitlist;

DROP SCHEMA IF EXISTS waitlist;

DROP FUNCTION IF EXISTS soft_delete_row();
DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
-- +goose StatementEnd
