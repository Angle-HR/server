-- name: InsertWaitlistEntry :exec
INSERT INTO waitlist (email, company_name, company_size, role)
VALUES ($1, $2, $3, $4);

-- name: WaitlistEmailExists :one
SELECT EXISTS (
    SELECT 1
    FROM waitlist
    WHERE email = $1
      AND deleted_at IS NULL
) AS exists;

-- name: CountWaitlistEntries :one
SELECT COUNT(*)::bigint
FROM waitlist
WHERE deleted_at IS NULL;
