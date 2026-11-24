-- Notifications service database (openhr_notifications). The service is an event consumer only: its inbox records
-- which (consumer, event id) pairs it has fully processed so a redelivered event does not send a second email.
-- Same shape as events_inbox in db/migrations/000018 (pkg/events/inbox); there is no outbox here because the
-- service publishes nothing. Nothing else belongs in this database.

-- +goose Up
-- +goose StatementBegin
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
-- +goose StatementEnd
