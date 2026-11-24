-- +goose Up
-- Employers may register in the United Kingdom and the European Union only for now. Countries in other regions stay
-- in the table (their rows are referenced) but are no longer offered or accepted at onboarding.
UPDATE waitlist.countries SET is_active = FALSE WHERE region NOT IN ('uk', 'eu');

-- +goose Down
UPDATE waitlist.countries SET is_active = TRUE WHERE region IN ('us', 'africa', 'asia');
