CREATE TABLE waitlist (
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
    BEFORE UPDATE ON waitlist
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TRIGGER waitlist_soft_delete
    BEFORE DELETE ON waitlist
    FOR EACH ROW
    EXECUTE FUNCTION soft_delete_row();
