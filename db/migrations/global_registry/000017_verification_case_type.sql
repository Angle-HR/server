-- Review queue: what kind of case an item is. 'business' is the company verification (KYB) every existing item is;
-- 'individual' is the identity review of a personal organization. Ids only, no personal data, as before.

-- +goose Up
-- +goose StatementBegin
ALTER TABLE admin.verification_queue
    ADD COLUMN IF NOT EXISTS case_type TEXT NOT NULL DEFAULT 'business';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'admin.verification_queue'::regclass AND conname = 'verification_queue_case_type_check'
    ) THEN
        ALTER TABLE admin.verification_queue
            ADD CONSTRAINT verification_queue_case_type_check CHECK (case_type IN ('business', 'individual'));
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE admin.verification_queue
    DROP CONSTRAINT IF EXISTS verification_queue_case_type_check,
    DROP COLUMN IF EXISTS case_type;
-- +goose StatementEnd
