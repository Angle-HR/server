-- name: ListSubmissions :many
SELECT
    ws.uuid,
    ws.name,
    ws.wants_early_access,
    ws.wants_user_testing,
    ws.submitted_at,
    r.uuid AS role_uuid,
    r.name AS role_name,
    ts.uuid AS team_size_uuid,
    ts.label AS team_size_label
FROM waitlist_submissions ws
INNER JOIN roles r ON r.id = ws.role_id
INNER JOIN team_sizes ts ON ts.id = ws.team_size_id
WHERE ws.deleted_at IS NULL
  AND (
    sqlc.narg(industry_uuid)::uuid IS NULL
    OR EXISTS (
        SELECT 1
        FROM submission_industries si
        INNER JOIN industries i ON i.id = si.industry_id
        WHERE si.submission_id = ws.id
          AND i.uuid = sqlc.narg(industry_uuid)::uuid
    )
  )
  AND (sqlc.narg(role_uuid)::uuid IS NULL OR r.uuid = sqlc.narg(role_uuid)::uuid)
  AND (sqlc.narg(team_size_uuid)::uuid IS NULL OR ts.uuid = sqlc.narg(team_size_uuid)::uuid)
  AND (sqlc.narg(wants_early_access)::boolean IS NULL OR ws.wants_early_access = sqlc.narg(wants_early_access)::boolean)
  AND (sqlc.narg(wants_user_testing)::boolean IS NULL OR ws.wants_user_testing = sqlc.narg(wants_user_testing)::boolean)
  AND (sqlc.narg(submitted_from)::timestamptz IS NULL OR ws.submitted_at >= sqlc.narg(submitted_from)::timestamptz)
  AND (sqlc.narg(submitted_to)::timestamptz IS NULL OR ws.submitted_at <= sqlc.narg(submitted_to)::timestamptz)
  AND (
    sqlc.narg(cursor_submitted_at)::timestamptz IS NULL
    OR sqlc.narg(cursor_id)::bigint IS NULL
    OR (ws.submitted_at, ws.id) < (sqlc.narg(cursor_submitted_at)::timestamptz, sqlc.narg(cursor_id)::bigint)
  )
ORDER BY ws.submitted_at DESC, ws.id DESC
LIMIT sqlc.arg(row_limit);

-- name: GetSubmissionInternalID :one
SELECT id
FROM waitlist_submissions
WHERE uuid = $1;

-- name: GetSubmissionDetail :one
SELECT
    ws.id,
    ws.uuid,
    ws.name,
    ws.wants_early_access,
    ws.wants_user_testing,
    ws.submitted_at,
    r.uuid AS role_uuid,
    r.name AS role_name,
    ts.uuid AS team_size_uuid,
    ts.label AS team_size_label
FROM waitlist_submissions ws
INNER JOIN roles r ON r.id = ws.role_id
INNER JOIN team_sizes ts ON ts.id = ws.team_size_id
WHERE ws.uuid = $1
  AND ws.deleted_at IS NULL;

-- name: ListSubmissionIndustries :many
SELECT i.uuid, i.name, si.other_text, i.slug
FROM submission_industries si
INNER JOIN industries i ON i.id = si.industry_id
WHERE si.submission_id = $1
ORDER BY i.sort_order, i.name;

-- name: ListSubmissionHiringTools :many
SELECT ht.uuid, ht.name, sht.other_text, ht.slug
FROM submission_hiring_tools sht
INNER JOIN hiring_tools ht ON ht.id = sht.hiring_tool_id
WHERE sht.submission_id = $1
ORDER BY ht.sort_order, ht.name;

-- name: ListSubmissionFrustrations :many
SELECT hf.uuid, hf.description, sf.other_text, hf.slug
FROM submission_frustrations sf
INNER JOIN hiring_frustrations hf ON hf.id = sf.frustration_id
WHERE sf.submission_id = $1
ORDER BY hf.sort_order, hf.description;

-- name: CreateAdminNote :one
INSERT INTO admin_notes (submission_id, note, created_by)
SELECT ws.id, $2, $3
FROM waitlist_submissions ws
WHERE ws.uuid = $1
  AND ws.deleted_at IS NULL
RETURNING uuid, note, created_by, created_at;

-- name: GetSubmissionStats :one
SELECT
    COUNT(*)::bigint AS total_submissions,
    COALESCE(AVG(CASE WHEN wants_early_access THEN 1.0 ELSE 0.0 END), 0)::float8 AS early_access_opt_in_rate,
    COALESCE(AVG(CASE WHEN wants_user_testing THEN 1.0 ELSE 0.0 END), 0)::float8 AS user_testing_opt_in_rate
FROM waitlist_submissions
WHERE deleted_at IS NULL;

-- name: ListTopIndustries :many
SELECT i.uuid, i.name, COUNT(*)::bigint AS count
FROM submission_industries si
INNER JOIN industries i ON i.id = si.industry_id
INNER JOIN waitlist_submissions ws ON ws.id = si.submission_id
WHERE ws.deleted_at IS NULL
GROUP BY i.uuid, i.name
ORDER BY COUNT(*) DESC, i.name
LIMIT 5;

-- name: ListTopFrustrations :many
SELECT hf.uuid, hf.description, COUNT(*)::bigint AS count
FROM submission_frustrations sf
INNER JOIN hiring_frustrations hf ON hf.id = sf.frustration_id
INNER JOIN waitlist_submissions ws ON ws.id = sf.submission_id
WHERE ws.deleted_at IS NULL
GROUP BY hf.uuid, hf.description
ORDER BY COUNT(*) DESC, hf.description
LIMIT 5;
