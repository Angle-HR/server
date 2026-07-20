ALTER TABLE users_registry
    DROP CONSTRAINT users_registry_region_check;

ALTER TABLE users_registry
    ADD CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));

ALTER TABLE countries
    DROP CONSTRAINT countries_region_check;

ALTER TABLE countries
    ADD CONSTRAINT countries_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));

ALTER TABLE tenant_subdomains
    DROP CONSTRAINT tenant_subdomains_region_check;

ALTER TABLE tenant_subdomains
    ADD CONSTRAINT tenant_subdomains_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));

UPDATE countries SET is_active = FALSE WHERE slug = 'south-africa';

INSERT INTO countries (id, name, slug, region, icon_key, sort_order) VALUES
    ('a7b8c9d0-e1f2-4345-a678-9abcdef01234', 'Germany', 'germany', 'eu', 'flag-de', 3),
    ('b8c9d0e1-f2a3-4456-b789-abcdef012345', 'India', 'india', 'asia', 'flag-in', 6)
ON CONFLICT (slug) DO UPDATE SET
    name = EXCLUDED.name,
    region = EXCLUDED.region,
    icon_key = EXCLUDED.icon_key,
    sort_order = EXCLUDED.sort_order,
    is_active = TRUE;

UPDATE countries SET sort_order = 1 WHERE slug = 'united-kingdom';
UPDATE countries SET sort_order = 2 WHERE slug = 'european-union';
UPDATE countries SET sort_order = 3 WHERE slug = 'germany';
UPDATE countries SET sort_order = 4 WHERE slug = 'united-states';
UPDATE countries SET sort_order = 5 WHERE slug = 'nigeria';
UPDATE countries SET sort_order = 6 WHERE slug = 'india';
UPDATE countries SET sort_order = 7 WHERE slug = 'kenya';
