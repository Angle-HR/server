-- +goose Up
-- +goose StatementBegin
ALTER TABLE auth.users_registry
    ADD COLUMN user_id UUID;

CREATE INDEX users_registry_user_id_idx ON auth.users_registry (user_id)
    WHERE user_id IS NOT NULL;

CREATE TABLE accounts.business_types (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX business_types_active_sort_idx ON accounts.business_types (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER business_types_set_updated_at
    BEFORE UPDATE ON accounts.business_types
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.onboarding_industries (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    emoji TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX onboarding_industries_active_sort_idx ON accounts.onboarding_industries (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER onboarding_industries_set_updated_at
    BEFORE UPDATE ON accounts.onboarding_industries
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE TABLE accounts.company_roles (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    icon_key TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX company_roles_active_sort_idx ON accounts.company_roles (sort_order)
    WHERE is_active = TRUE;

CREATE TRIGGER company_roles_set_updated_at
    BEFORE UPDATE ON accounts.company_roles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

INSERT INTO accounts.business_types (id, name, slug, sort_order) VALUES
    ('61000000-0000-4000-8000-000000000001', 'Early-stage startup', 'early-stage-startup', 1),
    ('61000000-0000-4000-8000-000000000002', 'Small business', 'small-business', 2),
    ('61000000-0000-4000-8000-000000000003', 'Mid-size company', 'mid-size-company', 3),
    ('61000000-0000-4000-8000-000000000004', 'Enterprise', 'enterprise', 4),
    ('61000000-0000-4000-8000-000000000005', 'Non-profit', 'non-profit', 5),
    ('61000000-0000-4000-8000-000000000006', 'Freelancer / agency', 'freelancer-agency', 6);

INSERT INTO accounts.onboarding_industries (id, name, slug, emoji, sort_order) VALUES
    ('62000000-0000-4000-8000-000000000001', 'Tech / Software', 'tech-software', E'💻', 1),
    ('62000000-0000-4000-8000-000000000002', 'Finance / Fintech', 'finance-fintech', E'💰', 2),
    ('62000000-0000-4000-8000-000000000003', 'Retail / E-commerce', 'retail-ecommerce', E'🛍️', 3),
    ('62000000-0000-4000-8000-000000000004', 'Hospitality / Food & Drink', 'hospitality-food-drink', E'🍽️', 4),
    ('62000000-0000-4000-8000-000000000005', 'Professional Services', 'professional-services', E'💼', 5),
    ('62000000-0000-4000-8000-000000000006', 'Beauty & Personal Care', 'beauty-personal-care', E'💅', 6),
    ('62000000-0000-4000-8000-000000000007', 'Logistics / Transport', 'logistics-transport', E'🚚', 7),
    ('62000000-0000-4000-8000-000000000008', 'Trades / Home Services', 'trades-home-services', E'🛠️', 8),
    ('62000000-0000-4000-8000-000000000009', 'Real Estate / Property', 'real-estate-property', E'🏠', 9),
    ('62000000-0000-4000-8000-00000000000a', 'Media / Creative', 'media-creative', E'🎨', 10),
    ('62000000-0000-4000-8000-00000000000b', 'Health', 'health', E'🩺', 11),
    ('62000000-0000-4000-8000-00000000000c', 'Education', 'education', E'📚', 12),
    ('62000000-0000-4000-8000-00000000000d', 'Agriculture', 'agriculture', E'🌾', 13),
    ('62000000-0000-4000-8000-00000000000e', 'Construction', 'construction', E'🏗️', 14),
    ('62000000-0000-4000-8000-00000000000f', 'Others', 'others', NULL, 15);

INSERT INTO accounts.company_roles (id, name, slug, icon_key, sort_order) VALUES
    ('60000000-0000-4000-8000-000000000001', 'Founder / CEO', 'founder-ceo', 'building', 1),
    ('60000000-0000-4000-8000-000000000002', 'Engineer / Designer', 'engineer-designer', 'code', 2),
    ('60000000-0000-4000-8000-000000000003', 'Marketing / Sales', 'marketing-sales', 'megaphone', 3),
    ('60000000-0000-4000-8000-000000000004', 'HR / People', 'hr-people', 'people', 4),
    ('60000000-0000-4000-8000-000000000005', 'Product', 'product', 'box', 5),
    ('60000000-0000-4000-8000-000000000006', 'Customer Support', 'customer-support', 'phone', 6),
    ('60000000-0000-4000-8000-000000000007', 'Operations', 'operations', 'globe', 7),
    ('60000000-0000-4000-8000-000000000008', 'Others', 'others', 'more', 8);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS accounts.company_roles;
DROP TABLE IF EXISTS accounts.onboarding_industries;
DROP TABLE IF EXISTS accounts.business_types;

DROP INDEX IF EXISTS auth.users_registry_user_id_idx;
ALTER TABLE auth.users_registry DROP COLUMN IF EXISTS user_id;
-- +goose StatementEnd
