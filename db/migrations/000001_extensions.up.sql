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
