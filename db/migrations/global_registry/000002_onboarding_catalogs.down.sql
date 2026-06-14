DROP TABLE IF EXISTS company_roles;
DROP TABLE IF EXISTS onboarding_industries;
DROP TABLE IF EXISTS business_types;

DROP INDEX IF EXISTS users_registry_user_id_idx;
ALTER TABLE users_registry DROP COLUMN IF EXISTS user_id;
