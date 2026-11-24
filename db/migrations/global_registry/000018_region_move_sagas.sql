-- Region-migration saga state (docs/microservices/org.md, "Region migration saga").
--
-- Moving an onboarding account out of the global holding area into its country's regional database spans identity
-- (the user row and users_registry) and org (the organization draft and onboarding progress). The org service drives
-- it and persists every step here so a crash or a failed call resumes or compensates instead of leaving a half-moved
-- account. One row per attempt; at most one live attempt per user.
--
-- The pending organization and progress rows used to hold a foreign key to accounts.pending_users. Identity now
-- releases the pending user at commit and org releases its own pending rows afterwards, so the key (a cross-service
-- coupling, like the ones dropped in migration 000019) goes.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS accounts.region_move_sagas (
    id            UUID PRIMARY KEY,
    user_id       UUID        NOT NULL,
    target_region TEXT        NOT NULL,
    -- started -> identity_prepared -> org_copied -> identity_committed -> completed (forward)
    -- started | identity_prepared | org_copied -> compensating -> compensated (backward, only before the commit)
    state         TEXT        NOT NULL DEFAULT 'started',
    attempts      INT         NOT NULL DEFAULT 0,
    last_error    TEXT        NOT NULL DEFAULT '',
    -- A worker holds the saga until lease_until; another process takes over after it.
    lease_until   TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ,
    CONSTRAINT region_move_sagas_state_check CHECK (state IN (
        'started', 'identity_prepared', 'org_copied', 'identity_committed', 'completed', 'compensating', 'compensated'
    )),
    CONSTRAINT region_move_sagas_region_check CHECK (target_region IN ('uk', 'us', 'africa', 'eu', 'asia'))
);

CREATE UNIQUE INDEX IF NOT EXISTS region_move_sagas_live_user_idx
    ON accounts.region_move_sagas (user_id) WHERE state NOT IN ('completed', 'compensated');
CREATE INDEX IF NOT EXISTS region_move_sagas_resume_idx
    ON accounts.region_move_sagas (updated_at) WHERE state NOT IN ('completed', 'compensated');

ALTER TABLE accounts.pending_organizations DROP CONSTRAINT IF EXISTS pending_organizations_owner_user_id_fkey;
ALTER TABLE accounts.pending_onboarding_progress DROP CONSTRAINT IF EXISTS pending_onboarding_progress_user_id_fkey;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS accounts.region_move_sagas;
-- The foreign keys are not restored: rows released out of order would make that fail.
-- +goose StatementEnd
