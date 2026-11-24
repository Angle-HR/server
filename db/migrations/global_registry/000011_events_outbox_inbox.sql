-- Event layer tables (pkg/events/outbox and pkg/events/inbox). Both are plain, schema-less tables in the
-- database's default schema, so every service database applies this file as it is. Nothing here touches the
-- existing hiring outbox (hiring.email_outbox or whatever the regional schema calls it); that keeps working as it is.
--
-- events_outbox: a domain event is INSERTed in the same transaction as the change that caused it; the relay
--                publishes it to the bus afterwards. id is the CloudEvents id (the bus de-duplicates on it).
-- events_inbox:  one row per (consumer, event id) a consumer has fully processed, written in the handler's own
--                transaction, so a redelivered event is skipped.

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS events_outbox (
    seq             BIGSERIAL PRIMARY KEY,
    id              TEXT        NOT NULL UNIQUE,
    type            TEXT        NOT NULL,
    partition_key   TEXT        NOT NULL DEFAULT '',
    envelope        JSONB       NOT NULL,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    failed_at       TIMESTAMPTZ
);

-- The relay's poll: only rows that are still waiting.
CREATE INDEX IF NOT EXISTS events_outbox_due_idx
    ON events_outbox (next_attempt_at, seq)
    WHERE sent_at IS NULL AND failed_at IS NULL;

-- Purging sent rows.
CREATE INDEX IF NOT EXISTS events_outbox_sent_idx
    ON events_outbox (sent_at)
    WHERE sent_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS events_inbox (
    consumer     TEXT        NOT NULL,
    event_id     TEXT        NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

CREATE INDEX IF NOT EXISTS events_inbox_processed_idx ON events_inbox (processed_at);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS events_inbox;
DROP TABLE IF EXISTS events_outbox;
-- +goose StatementEnd
