-- name: CreateSession :one
INSERT INTO bosun.sessions (
    id, user_id, cr_namespace, cr_name, tier, runtime, provider_mode,
    provider_credential_id, storage_policy, desired_state, resume_nonce,
    phase, phase_reason, conditions, last_active_at, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
    $12, $13, $14, $15, $16, $16
)
RETURNING *;

-- name: InsertSessionEvent :exec
INSERT INTO bosun.session_events (id, session_id, type, payload, occurred_at)
VALUES ($1, $2, $3, $4, $5);

-- name: CountActiveSessionsForUser :one
SELECT count(*)
FROM bosun.sessions
WHERE user_id = $1
  AND deleted_at IS NULL
  AND phase IN ('Pending', 'Provisioning', 'Running', 'Idle', 'Hibernating', 'Restoring');

-- name: GetSessionForUser :one
SELECT *
FROM bosun.sessions
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: GetSessionByID :one
SELECT *
FROM bosun.sessions
WHERE id = $1;

-- name: ListSessionsForUser :many
SELECT *
FROM bosun.sessions
WHERE user_id = $1 AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT $2 OFFSET $3;

-- name: CountSessionsForUser :one
SELECT count(*)
FROM bosun.sessions
WHERE user_id = $1 AND deleted_at IS NULL;

-- name: ListPendingSessions :many
SELECT *
FROM bosun.sessions
WHERE deleted_at IS NULL AND phase = 'Pending'
ORDER BY created_at
LIMIT $1;

-- name: ListDeletingSessions :many
SELECT *
FROM bosun.sessions
WHERE deleted_at IS NOT NULL AND phase_reason <> 'CleanupComplete'
ORDER BY deleted_at
LIMIT $1;

-- name: UpdateSessionDesiredState :one
UPDATE bosun.sessions
SET desired_state = $3,
    resume_nonce = $4,
    last_active_at = $5,
    updated_at = $6,
    version = version + 1
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL AND version = $7
RETURNING *;

-- name: SoftDeleteSession :one
UPDATE bosun.sessions
SET desired_state = 'Hibernated',
    phase = 'Deleting',
    phase_reason = 'DeleteRequested',
    deleted_at = $3,
    updated_at = $3,
    version = version + 1
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: ProjectSessionStatus :one
UPDATE bosun.sessions
SET phase = $2,
    phase_reason = $3,
    conditions = $4,
    last_active_at = $5,
    cr_resource_version = $6,
    updated_at = $7,
    version = version + 1
WHERE id = $1 AND cr_resource_version < $6
RETURNING *;

-- name: MarkSessionCleanupComplete :execrows
UPDATE bosun.sessions
SET phase_reason = 'CleanupComplete',
    updated_at = $2,
    version = version + 1
WHERE id = $1 AND deleted_at IS NOT NULL AND phase_reason <> 'CleanupComplete';
