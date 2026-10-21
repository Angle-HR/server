-- A retry with the same Idempotency-Key has to be told apart from a different request that reuses the key. The
-- fingerprint is a SHA-256 of what the candidate sent; it is compared, never read back. Rows saved before this
-- migration have none, and a retry of one of them is answered as a repeat.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE hiring.applications ADD COLUMN idempotency_fingerprint TEXT;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE hiring.applications DROP COLUMN idempotency_fingerprint;
-- +goose StatementEnd
