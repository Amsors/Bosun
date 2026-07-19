-- name: CreateUser :one
INSERT INTO bosun.users (id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING id, email, password_hash, created_at, disabled_at;

-- name: GetUserByEmail :one
SELECT id, email, password_hash, created_at, disabled_at
FROM bosun.users
WHERE lower(email) = lower($1);

-- name: GetUserByID :one
SELECT id, email, password_hash, created_at, disabled_at
FROM bosun.users
WHERE id = $1;

-- name: UpdateUserPasswordHash :exec
UPDATE bosun.users
SET password_hash = $2
WHERE id = $1;

-- name: ListUserIDs :many
SELECT id
FROM bosun.users
WHERE disabled_at IS NULL
ORDER BY created_at;
