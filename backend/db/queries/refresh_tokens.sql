-- name: InsertRefreshToken :exec
INSERT INTO bosun.refresh_tokens (id, user_id, family_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4, $5);

-- name: GetRefreshTokenByHash :one
SELECT id, user_id, family_id, token_hash, issued_at, expires_at, revoked_at, replaced_by
FROM bosun.refresh_tokens
WHERE token_hash = $1;

-- RotateRefreshToken 以原子更新保证同一 token 只有一个轮换成功：仅当尚未撤销时置为已撤销，
-- 返回受影响行以判定胜出者，落败的并发请求按重用处理并撤销 family。
-- name: RotateRefreshToken :one
UPDATE bosun.refresh_tokens
SET revoked_at = now(), replaced_by = $2
WHERE id = $1 AND revoked_at IS NULL
RETURNING id;

-- name: RevokeRefreshTokenFamily :exec
UPDATE bosun.refresh_tokens
SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;
