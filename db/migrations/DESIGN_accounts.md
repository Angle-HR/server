# Accounts schema design

Design document for product onboarding persistence. Migrations are implemented as `000002_accounts.up.sql` (regional) and `global_registry/000002_onboarding_catalogs.up.sql` (global).

## Architecture

```
┌─────────────────────────────────────┐
│  Global Postgres (DB_URL_GLOBAL)    │
│  users_registry (+ user_id column)  │
│  business_types                     │
│  onboarding_industries              │
│  company_roles                      │
│  countries (existing)               │
└──────────────┬──────────────────────┘
               │ email + user_id + region
               ▼
┌─────────────────────────────────────┐
│  Regional Postgres (×4 regions)     │
│  accounts.users                     │
│  accounts.organizations             │
│  accounts.addresses                 │
│  accounts.onboarding_progress       │
│  waitlist.* (existing, unchanged)   │
└─────────────────────────────────────┘
```

Password hashes and profile data live in the **regional** database for data residency. The global registry holds email uniqueness and region routing only.

## Global registry changes

### Extend users_registry

```sql
ALTER TABLE users_registry
    ADD COLUMN user_id UUID;

CREATE INDEX users_registry_user_id_idx ON users_registry (user_id)
    WHERE user_id IS NOT NULL;
```

- `waitlist_token` continues to serve waitlist signups.
- `user_id` links to `accounts.users.id` in the user's regional database.
- One email row may eventually hold both `waitlist_token` and `user_id` if a waitlist user later creates a product account.

### business_types

```sql
CREATE TABLE business_types (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX business_types_active_sort_idx ON business_types (sort_order)
    WHERE is_active = TRUE;
```

Seed data: see `db/migrations/global_registry/000002_onboarding_catalogs.up.sql`.

### onboarding_industries

```sql
CREATE TABLE onboarding_industries (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    emoji TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX onboarding_industries_active_sort_idx ON onboarding_industries (sort_order)
    WHERE is_active = TRUE;
```

Distinct from waitlist `industries` table (different labels and UUIDs).

### company_roles

```sql
CREATE TABLE company_roles (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT NOT NULL UNIQUE,
    icon_key TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX company_roles_active_sort_idx ON company_roles (sort_order)
    WHERE is_active = TRUE;
```

Distinct from waitlist `roles` table.

## Regional schema: accounts

Apply to each regional database. Update `dbrouter` search path to include `accounts` (e.g. `accounts,waitlist,public`).

### accounts.users

```sql
CREATE SCHEMA IF NOT EXISTS accounts;

CREATE TABLE accounts.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    email_verified_at TIMESTAMPTZ,
    onboarding_completed_at TIMESTAMPTZ,
    account_type TEXT,
    first_name TEXT,
    last_name TEXT,
    legal_full_name TEXT,
    country_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deleted_at TIMESTAMPTZ,
    CONSTRAINT accounts_users_account_type_check
        CHECK (account_type IS NULL OR account_type IN ('individual', 'business'))
);

CREATE INDEX accounts_users_email_idx ON accounts.users (email)
    WHERE deleted_at IS NULL;

CREATE TRIGGER accounts_users_set_updated_at
    BEFORE UPDATE ON accounts.users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
```

| Column | Set when |
|--------|----------|
| `email`, `password_hash` | `POST /auth/signup` |
| `email_verified_at` | `POST /auth/verify-email` |
| `account_type`, name fields, `country_id` | `PUT /onboarding/profile` |
| `onboarding_completed_at` | `POST /onboarding/complete` |

### accounts.organizations

One organization per business user during onboarding.

```sql
CREATE TABLE accounts.organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id UUID NOT NULL REFERENCES accounts.users (id),
    legal_name TEXT NOT NULL,
    company_role_id UUID NOT NULL,
    business_type_id UUID,
    industry_id UUID,
    employee_count INTEGER,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_organizations_employee_count_check
        CHECK (employee_count IS NULL OR (employee_count >= 1 AND employee_count <= 10000))
);

CREATE UNIQUE INDEX accounts_organizations_owner_idx ON accounts.organizations (owner_user_id);

CREATE TRIGGER accounts_organizations_set_updated_at
    BEFORE UPDATE ON accounts.organizations
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
```

| Column | Set when |
|--------|----------|
| `legal_name`, `company_role_id` | `PUT /onboarding/profile` (business) |
| `business_type_id`, `industry_id`, `employee_count` | `PUT /onboarding/business` |

Catalog UUIDs are validated against global tables at write time.

### accounts.addresses

```sql
CREATE TABLE accounts.addresses (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES accounts.users (id),
    organization_id UUID REFERENCES accounts.organizations (id),
    country_id UUID NOT NULL,
    entry_mode TEXT NOT NULL,
    line_1 TEXT NOT NULL,
    line_2 TEXT,
    city TEXT NOT NULL,
    state_or_county TEXT NOT NULL,
    post_code TEXT NOT NULL,
    formatted_address TEXT,
    verification_status TEXT NOT NULL DEFAULT 'unverified',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT accounts_addresses_entry_mode_check
        CHECK (entry_mode IN ('search', 'manual')),
    CONSTRAINT accounts_addresses_verification_status_check
        CHECK (verification_status IN ('unverified', 'verified', 'failed'))
);

CREATE UNIQUE INDEX accounts_addresses_user_idx ON accounts.addresses (user_id);

CREATE TRIGGER accounts_addresses_set_updated_at
    BEFORE UPDATE ON accounts.addresses
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
```

### accounts.onboarding_progress

```sql
CREATE TABLE accounts.onboarding_progress (
    user_id UUID PRIMARY KEY REFERENCES accounts.users (id),
    current_step TEXT NOT NULL,
    completed_steps TEXT[] NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER accounts_onboarding_progress_set_updated_at
    BEFORE UPDATE ON accounts.onboarding_progress
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
```

| Step value | Meaning |
|------------|---------|
| `verify_email` | Email verified |
| `profile` | Account type and names saved |
| `address` | Address saved |
| `business` | Business compliance saved |
| `complete` | Ready for `POST /onboarding/complete` |

## Region assignment

1. On `POST /auth/signup`, user is created in a **default region** (e.g. `uk`) or region inferred from GeoIP if available.
2. On `PUT /onboarding/profile` with `country_id` (individual), region is updated from `countries.region` (same pattern as waitlist signup).
3. `users_registry.region` is written/updated on first profile save.

## Signup flow data writes

| Action | Global DB | Regional DB | Redis |
|--------|-----------|-------------|-------|
| `POST /auth/signup` | — | INSERT `accounts.users` | SET OTP |
| `POST /auth/verify-email` | INSERT/UPDATE `users_registry` | SET `email_verified_at`, INSERT `onboarding_progress` | DEL OTP |
| `PUT /onboarding/profile` | UPDATE `users_registry.region` | UPDATE user + org | — |
| `PUT /onboarding/address` | — | UPSERT address, UPDATE progress | — |
| `PUT /onboarding/business` | — | UPDATE org, UPDATE progress | — |
| `POST /onboarding/complete` | — | SET `onboarding_completed_at` | — |

## What stays unchanged

- `waitlist` schema and all waitlist tables
- Waitlist catalog tables (`industries`, `roles`, `team_sizes`, etc.)
- `tenant_subdomains` (workspace stub may populate this in implementation phase)

## Implementation checklist

1. Add global migration `000002_onboarding_catalogs.up.sql` with catalog tables and seed INSERTs.
2. Add regional migration `000002_accounts.up.sql` with `accounts` schema.
3. Update `internal/dbrouter/router.go` search path.
4. Add query builders in `internal/query/`.
5. Document endpoints in handler godoc (`@Summary`, `@Description`, `@Tags`) and run `make swagger`.
