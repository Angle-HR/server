-- name: ListIndustries :many
SELECT uuid, name, slug, COALESCE(icon_key, '') AS icon_key, sort_order
FROM industries
WHERE is_active = TRUE
ORDER BY sort_order, name;

-- name: ListHiringTools :many
SELECT uuid, name, slug, COALESCE(icon_key, '') AS icon_key, COALESCE(category, '') AS category, sort_order
FROM hiring_tools
WHERE is_active = TRUE
ORDER BY sort_order, name;

-- name: ListHiringFrustrations :many
SELECT uuid, description, slug, sort_order
FROM hiring_frustrations
WHERE is_active = TRUE
ORDER BY sort_order, description;

-- name: ListRoles :many
SELECT uuid, name, slug, sort_order
FROM roles
WHERE is_active = TRUE
ORDER BY sort_order, name;

-- name: ListTeamSizes :many
SELECT uuid, label, min_size, max_size, sort_order
FROM team_sizes
ORDER BY sort_order, label;

-- name: IndustriesReferenceVersion :one
SELECT COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version
FROM industries;

-- name: HiringToolsReferenceVersion :one
SELECT COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version
FROM hiring_tools;

-- name: HiringFrustrationsReferenceVersion :one
SELECT COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version
FROM hiring_frustrations;

-- name: RolesReferenceVersion :one
SELECT COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version
FROM roles;

-- name: TeamSizesReferenceVersion :one
SELECT COALESCE(MAX(updated_at), to_timestamp(0))::text || ':' || COUNT(*)::text AS version
FROM team_sizes;

-- name: ResolveIndustryIDs :many
SELECT id, uuid, slug
FROM industries
WHERE uuid = ANY($1::uuid[])
  AND is_active = TRUE;

-- name: ResolveHiringToolIDs :many
SELECT id, uuid, slug
FROM hiring_tools
WHERE uuid = ANY($1::uuid[])
  AND is_active = TRUE;

-- name: ResolveFrustrationIDs :many
SELECT id, uuid, slug
FROM hiring_frustrations
WHERE uuid = ANY($1::uuid[])
  AND is_active = TRUE;

-- name: ResolveRoleID :one
SELECT id
FROM roles
WHERE uuid = $1
  AND is_active = TRUE;

-- name: ResolveTeamSizeID :one
SELECT id
FROM team_sizes
WHERE uuid = $1;

-- name: UUIDsForIndustryIDs :many
SELECT id, uuid
FROM industries
WHERE id = ANY($1::bigint[]);

-- name: UUIDsForHiringToolIDs :many
SELECT id, uuid
FROM hiring_tools
WHERE id = ANY($1::bigint[]);

-- name: UUIDsForFrustrationIDs :many
SELECT id, uuid
FROM hiring_frustrations
WHERE id = ANY($1::bigint[]);

-- name: UUIDForRoleID :one
SELECT uuid
FROM roles
WHERE id = $1;

-- name: UUIDForTeamSizeID :one
SELECT uuid
FROM team_sizes
WHERE id = $1;
