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
    email TEXT NOT NULL UNIQUE,
    company_name VARCHAR,
    company_size VARCHAR,
    role VARCHAR,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ
);

CREATE TRIGGER waitlist_set_updated_at
    BEFORE UPDATE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_soft_delete
    BEFORE DELETE ON waitlist.waitlist
    FOR EACH ROW
    EXECUTE FUNCTION soft_delete_row();

CREATE TABLE waitlist.industries (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    icon_key TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT industries_uuid_key UNIQUE (uuid),
    CONSTRAINT industries_slug_key UNIQUE (slug)
);

CREATE INDEX industries_uuid_idx ON waitlist.industries (uuid);
CREATE INDEX industries_slug_idx ON waitlist.industries (slug);

CREATE TABLE waitlist.hiring_tools (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    icon_key TEXT,
    category TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hiring_tools_uuid_key UNIQUE (uuid),
    CONSTRAINT hiring_tools_slug_key UNIQUE (slug)
);

CREATE INDEX hiring_tools_uuid_idx ON waitlist.hiring_tools (uuid);
CREATE INDEX hiring_tools_slug_idx ON waitlist.hiring_tools (slug);

CREATE TABLE waitlist.hiring_frustrations (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    description TEXT NOT NULL,
    slug TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hiring_frustrations_uuid_key UNIQUE (uuid),
    CONSTRAINT hiring_frustrations_slug_key UNIQUE (slug)
);

CREATE INDEX hiring_frustrations_uuid_idx ON waitlist.hiring_frustrations (uuid);
CREATE INDEX hiring_frustrations_slug_idx ON waitlist.hiring_frustrations (slug);

CREATE TABLE waitlist.roles (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    slug TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT roles_uuid_key UNIQUE (uuid),
    CONSTRAINT roles_slug_key UNIQUE (slug)
);

CREATE INDEX roles_uuid_idx ON waitlist.roles (uuid);
CREATE INDEX roles_slug_idx ON waitlist.roles (slug);

CREATE TABLE waitlist.team_sizes (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    label TEXT NOT NULL,
    min_size INTEGER,
    max_size INTEGER,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT team_sizes_uuid_key UNIQUE (uuid),
    CONSTRAINT team_sizes_range_check CHECK (
        min_size IS NULL OR max_size IS NULL OR min_size <= max_size
    )
);

CREATE INDEX team_sizes_uuid_idx ON waitlist.team_sizes (uuid);

CREATE TABLE waitlist.waitlist_submissions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    wants_early_access BOOLEAN NOT NULL DEFAULT FALSE,
    wants_user_testing BOOLEAN NOT NULL DEFAULT FALSE,
    role_id BIGINT NOT NULL REFERENCES waitlist.roles (id),
    team_size_id BIGINT NOT NULL REFERENCES waitlist.team_sizes (id),
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT waitlist_submissions_uuid_key UNIQUE (uuid)
);

CREATE INDEX waitlist_submissions_uuid_idx ON waitlist.waitlist_submissions (uuid);
CREATE INDEX waitlist_submissions_role_id_idx ON waitlist.waitlist_submissions (role_id);
CREATE INDEX waitlist_submissions_team_size_id_idx ON waitlist.waitlist_submissions (team_size_id);
CREATE INDEX waitlist_submissions_active_idx ON waitlist.waitlist_submissions (submitted_at)
    WHERE deleted_at IS NULL;

CREATE TABLE waitlist.submission_industries (
    submission_id BIGINT NOT NULL REFERENCES waitlist.waitlist_submissions (id) ON DELETE CASCADE,
    industry_id BIGINT NOT NULL REFERENCES waitlist.industries (id),
    other_text TEXT,
    PRIMARY KEY (submission_id, industry_id)
);

CREATE INDEX submission_industries_submission_id_idx ON waitlist.submission_industries (submission_id);
CREATE INDEX submission_industries_industry_id_idx ON waitlist.submission_industries (industry_id);

CREATE TABLE waitlist.submission_hiring_tools (
    submission_id BIGINT NOT NULL REFERENCES waitlist.waitlist_submissions (id) ON DELETE CASCADE,
    hiring_tool_id BIGINT NOT NULL REFERENCES waitlist.hiring_tools (id),
    other_text TEXT,
    PRIMARY KEY (submission_id, hiring_tool_id)
);

CREATE INDEX submission_hiring_tools_submission_id_idx ON waitlist.submission_hiring_tools (submission_id);
CREATE INDEX submission_hiring_tools_hiring_tool_id_idx ON waitlist.submission_hiring_tools (hiring_tool_id);

CREATE TABLE waitlist.submission_frustrations (
    submission_id BIGINT NOT NULL REFERENCES waitlist.waitlist_submissions (id) ON DELETE CASCADE,
    frustration_id BIGINT NOT NULL REFERENCES waitlist.hiring_frustrations (id),
    other_text TEXT,
    PRIMARY KEY (submission_id, frustration_id)
);

CREATE INDEX submission_frustrations_submission_id_idx ON waitlist.submission_frustrations (submission_id);
CREATE INDEX submission_frustrations_frustration_id_idx ON waitlist.submission_frustrations (frustration_id);

CREATE TABLE waitlist.submission_sessions (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    token_hash BYTEA NOT NULL,
    current_step SMALLINT NOT NULL DEFAULT 1,
    expires_at TIMESTAMPTZ NOT NULL,
    partial_state JSONB NOT NULL DEFAULT '{}'::jsonb,
    completed_at TIMESTAMPTZ,
    submission_id BIGINT REFERENCES waitlist.waitlist_submissions (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT submission_sessions_uuid_key UNIQUE (uuid),
    CONSTRAINT submission_sessions_token_hash_key UNIQUE (token_hash),
    CONSTRAINT submission_sessions_current_step_check CHECK (current_step BETWEEN 1 AND 5)
);

CREATE INDEX submission_sessions_uuid_idx ON waitlist.submission_sessions (uuid);
CREATE INDEX submission_sessions_expires_at_idx ON waitlist.submission_sessions (expires_at);

CREATE TABLE waitlist.admin_notes (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    uuid UUID NOT NULL DEFAULT gen_random_uuid(),
    submission_id BIGINT NOT NULL REFERENCES waitlist.waitlist_submissions (id) ON DELETE CASCADE,
    note TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT admin_notes_uuid_key UNIQUE (uuid)
);

CREATE INDEX admin_notes_uuid_idx ON waitlist.admin_notes (uuid);
CREATE INDEX admin_notes_submission_id_idx ON waitlist.admin_notes (submission_id);

CREATE TRIGGER industries_set_updated_at
    BEFORE UPDATE ON waitlist.industries
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER hiring_tools_set_updated_at
    BEFORE UPDATE ON waitlist.hiring_tools
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER hiring_frustrations_set_updated_at
    BEFORE UPDATE ON waitlist.hiring_frustrations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER roles_set_updated_at
    BEFORE UPDATE ON waitlist.roles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER team_sizes_set_updated_at
    BEFORE UPDATE ON waitlist.team_sizes
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_submissions_set_updated_at
    BEFORE UPDATE ON waitlist.waitlist_submissions
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_submissions_soft_delete
    BEFORE DELETE ON waitlist.waitlist_submissions
    FOR EACH ROW
    EXECUTE FUNCTION soft_delete_row();

CREATE TRIGGER submission_sessions_set_updated_at
    BEFORE UPDATE ON waitlist.submission_sessions
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER admin_notes_set_updated_at
    BEFORE UPDATE ON waitlist.admin_notes
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
