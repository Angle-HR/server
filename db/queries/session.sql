-- name: CreateSubmissionSession :one
INSERT INTO submission_sessions (token_hash, current_step, expires_at, partial_state)
VALUES ($1, 1, $2, '{}'::jsonb)
RETURNING id, current_step, expires_at, partial_state, completed_at;

-- name: GetSubmissionSessionByToken :one
SELECT id, current_step, expires_at, partial_state, completed_at
FROM submission_sessions
WHERE token_hash = $1;

-- name: UpdateSubmissionSessionStep :execrows
UPDATE submission_sessions
SET partial_state = $2::jsonb,
    current_step = $3,
    updated_at = now()
WHERE id = $1
  AND completed_at IS NULL;

-- name: InsertWaitlistSubmission :one
INSERT INTO waitlist_submissions (
    name, wants_early_access, wants_user_testing, role_id, team_size_id
)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, uuid, submitted_at;

-- name: InsertSubmissionIndustry :exec
INSERT INTO submission_industries (submission_id, industry_id, other_text)
VALUES ($1, $2, $3);

-- name: InsertSubmissionHiringTool :exec
INSERT INTO submission_hiring_tools (submission_id, hiring_tool_id, other_text)
VALUES ($1, $2, $3);

-- name: InsertSubmissionFrustration :exec
INSERT INTO submission_frustrations (submission_id, frustration_id, other_text)
VALUES ($1, $2, $3);

-- name: GetOtherIndustryID :one
SELECT id
FROM industries
WHERE slug = 'other';

-- name: GetOtherHiringToolID :one
SELECT id
FROM hiring_tools
WHERE slug = 'other';

-- name: GetOtherFrustrationID :one
SELECT id
FROM hiring_frustrations
WHERE slug = 'other';

-- name: CompleteSubmissionSession :execrows
UPDATE submission_sessions
SET completed_at = now(),
    submission_id = $2,
    updated_at = now()
WHERE id = $1
  AND completed_at IS NULL;
