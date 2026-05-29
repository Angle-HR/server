DROP TRIGGER IF EXISTS users_registry_set_updated_at ON users_registry;

DROP TABLE IF EXISTS tenant_subdomains;
DROP TABLE IF EXISTS waitlist;
DROP TABLE IF EXISTS users_registry;

DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
