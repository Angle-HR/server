-- +goose Up
-- Jobs can be open in the United Kingdom and the European Union only for now. An "Anywhere" job spans the open
-- markets, so the other markets are closed here (their rows stay: existing jobs and applications refer to them).
UPDATE hiring.markets SET is_open = (code IN ('UK', 'EU'));

-- +goose Down
UPDATE hiring.markets SET is_open = (code IN ('UK', 'US', 'PH'));
