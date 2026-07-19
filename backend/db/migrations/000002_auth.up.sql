-- P0 认证与幂等数据表（techspec §6）。所有对象位于 bosun schema。

CREATE TABLE bosun.users (
    id            uuid PRIMARY KEY,
    email         text NOT NULL,
    password_hash text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    disabled_at   timestamptz
);

-- 邮箱不区分大小写唯一（techspec §6）。
CREATE UNIQUE INDEX users_email_lower_key ON bosun.users (lower(email));

CREATE TABLE bosun.refresh_tokens (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES bosun.users (id) ON DELETE CASCADE,
    family_id   uuid NOT NULL,
    token_hash  bytea NOT NULL,
    issued_at   timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    revoked_at  timestamptz,
    replaced_by uuid REFERENCES bosun.refresh_tokens (id)
);

-- 只存高熵 token 的 SHA-256，禁止明文；hash 唯一用于快速定位与重用检测。
CREATE UNIQUE INDEX refresh_tokens_token_hash_key ON bosun.refresh_tokens (token_hash);
CREATE INDEX refresh_tokens_family_id_idx ON bosun.refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_id_idx ON bosun.refresh_tokens (user_id);

-- 幂等键去重（techspec §6，保留 24 小时）。匿名请求（register）使用全零 UUID 作为 scope，
-- 真实用户 id 为 UUID v7 不会与之冲突；避免依赖 PostgreSQL 15+ 的 NULLS NOT DISTINCT。
CREATE TABLE bosun.idempotency_keys (
    user_id         uuid NOT NULL,
    key             text NOT NULL,
    method          text NOT NULL,
    path            text NOT NULL,
    request_hash    bytea NOT NULL,
    response_status integer NOT NULL,
    response_body   bytea NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    PRIMARY KEY (user_id, key)
);

CREATE INDEX idempotency_keys_expires_at_idx ON bosun.idempotency_keys (expires_at);
