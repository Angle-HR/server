-- +goose Up
-- +goose StatementBegin
CREATE TABLE waitlist.business_types (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX business_types_active_sort_idx ON waitlist.business_types (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER business_types_set_updated_at
    BEFORE UPDATE ON waitlist.business_types
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO waitlist.business_types (id, name, slug, sort_order) VALUES
    ('70000000-0000-4000-8000-000000000001', 'Early-stage startup', 'early-stage-startup', 1),
    ('70000000-0000-4000-8000-000000000002', 'Small business (1–50 employees)', 'small-business', 2),
    ('70000000-0000-4000-8000-000000000003', 'Growing company (51–250 employees)', 'growing-company', 3),
    ('70000000-0000-4000-8000-000000000004', 'Agency / Studio', 'agency-studio', 4),
    ('70000000-0000-4000-8000-000000000005', 'Nonprofit', 'nonprofit', 5),
    ('70000000-0000-4000-8000-000000000006', 'Sole Trader', 'sole-trader', 6);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS business_types_set_updated_at ON waitlist.business_types;
DROP TABLE IF EXISTS waitlist.business_types;
-- +goose StatementEnd
