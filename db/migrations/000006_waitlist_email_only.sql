-- +goose Up
-- +goose StatementBegin
ALTER TABLE waitlist.waitlist
    ALTER COLUMN full_name DROP NOT NULL,
    ALTER COLUMN country_id DROP NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE waitlist.waitlist
    ALTER COLUMN full_name SET NOT NULL,
    ALTER COLUMN country_id SET NOT NULL;
-- +goose StatementEnd
