-- +goose Up
-- +goose StatementBegin
ALTER TABLE waitlist.waitlist
    DROP CONSTRAINT waitlist_region_check;

ALTER TABLE waitlist.waitlist
    ADD CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu', 'asia'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE waitlist.waitlist
    DROP CONSTRAINT waitlist_region_check;

ALTER TABLE waitlist.waitlist
    ADD CONSTRAINT waitlist_region_check
        CHECK (region IN ('uk', 'us', 'africa', 'eu'));
-- +goose StatementEnd
