-- Opens the Philippines for hiring. PH has a storage region (asia) and no row in hiring.platform_prerequisites.
-- It has no seeded compliance gates or selection warnings yet; add them in a follow-up once counsel has reviewed.
-- The other closed markets stay closed: EU, SG, IN and AE have unfinished prerequisites, and NG, KE and ZA have no
-- storage region (the residency decision is still open), so the markets_open_needs_region_check constraint refuses them.

-- +goose Up
-- +goose StatementBegin
UPDATE hiring.markets SET is_open = TRUE WHERE code = 'PH';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
UPDATE hiring.markets SET is_open = FALSE WHERE code = 'PH';
-- +goose StatementEnd
