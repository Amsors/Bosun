-- name: GetIdempotencyKey :one
SELECT user_id, key, method, path, request_hash, response_status, response_body, created_at, expires_at
FROM bosun.idempotency_keys
WHERE user_id = $1 AND key = $2 AND expires_at > now();

-- InsertIdempotencyKey 在 advisory lock 保护下写入；已过期记录允许原子替换。
-- name: InsertIdempotencyKey :execrows
INSERT INTO bosun.idempotency_keys (
    user_id, key, method, path, request_hash, response_status, response_body, expires_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (user_id, key) DO UPDATE SET
    method = EXCLUDED.method,
    path = EXCLUDED.path,
    request_hash = EXCLUDED.request_hash,
    response_status = EXCLUDED.response_status,
    response_body = EXCLUDED.response_body,
    created_at = now(),
    expires_at = EXCLUDED.expires_at
WHERE bosun.idempotency_keys.expires_at <= now();

-- name: DeleteExpiredIdempotencyKeys :exec
DELETE FROM bosun.idempotency_keys
WHERE expires_at < now();
