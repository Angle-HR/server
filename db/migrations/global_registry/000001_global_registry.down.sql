DROP TRIGGER IF EXISTS team_sizes_set_updated_at ON team_sizes;
DROP TRIGGER IF EXISTS roles_set_updated_at ON roles;
DROP TRIGGER IF EXISTS hiring_frustrations_set_updated_at ON hiring_frustrations;
DROP TRIGGER IF EXISTS hiring_tools_set_updated_at ON hiring_tools;
DROP TRIGGER IF EXISTS industries_set_updated_at ON industries;
DROP TRIGGER IF EXISTS countries_set_updated_at ON countries;
DROP TRIGGER IF EXISTS users_registry_set_updated_at ON users_registry;

DROP TABLE IF EXISTS team_sizes;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS hiring_frustrations;
DROP TABLE IF EXISTS hiring_tools;
DROP TABLE IF EXISTS industries;
DROP TABLE IF EXISTS tenant_subdomains;
DROP TABLE IF EXISTS countries;
DROP TABLE IF EXISTS users_registry;

DROP FUNCTION IF EXISTS set_updated_at();
DROP EXTENSION IF EXISTS pgcrypto;
