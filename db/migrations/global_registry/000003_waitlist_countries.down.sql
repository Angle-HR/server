DELETE FROM countries WHERE slug IN ('germany', 'india');

UPDATE countries SET is_active = TRUE, sort_order = 6 WHERE slug = 'south-africa';

UPDATE countries SET sort_order = 1 WHERE slug = 'united-kingdom';
UPDATE countries SET sort_order = 2 WHERE slug = 'european-union';
UPDATE countries SET sort_order = 3 WHERE slug = 'united-states';
UPDATE countries SET sort_order = 4 WHERE slug = 'nigeria';
UPDATE countries SET sort_order = 5 WHERE slug = 'kenya';

ALTER TABLE users_registry
    DROP CONSTRAINT users_registry_region_check;

ALTER TABLE users_registry
    ADD CONSTRAINT users_registry_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'));

ALTER TABLE countries
    DROP CONSTRAINT countries_region_check;

ALTER TABLE countries
    ADD CONSTRAINT countries_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'));

ALTER TABLE tenant_subdomains
    DROP CONSTRAINT tenant_subdomains_region_check;

ALTER TABLE tenant_subdomains
    ADD CONSTRAINT tenant_subdomains_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'));
