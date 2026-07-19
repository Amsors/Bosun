-- P0 session lifecycle projection and append-only audit events (techspec §6).

CREATE TABLE bosun.sessions (
    id                     uuid PRIMARY KEY,
    user_id                uuid NOT NULL REFERENCES bosun.users (id) ON DELETE CASCADE,
    cr_namespace           text NOT NULL,
    cr_name                text NOT NULL,
    tier                   text NOT NULL CHECK (tier IN ('small', 'medium')),
    runtime                text NOT NULL CHECK (runtime = 'claude-code'),
    provider_mode          text NOT NULL CHECK (provider_mode IN ('platform', 'byok')),
    provider_credential_id uuid,
    storage_policy         text NOT NULL CHECK (storage_policy IN ('local', 'archive')),
    desired_state          text NOT NULL CHECK (desired_state IN ('Running', 'Hibernated')),
    resume_nonce           uuid NOT NULL,
    phase                  text NOT NULL,
    phase_reason           text NOT NULL DEFAULT '',
    conditions             jsonb NOT NULL DEFAULT '[]'::jsonb,
    last_active_at         timestamptz,
    cr_resource_version    bigint NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    deleted_at             timestamptz,
    version                bigint NOT NULL DEFAULT 1,
    UNIQUE (cr_namespace, cr_name)
);

CREATE INDEX sessions_user_created_idx ON bosun.sessions (user_id, created_at DESC);
CREATE INDEX sessions_pending_idx ON bosun.sessions (phase) WHERE deleted_at IS NULL AND phase = 'Pending';
CREATE INDEX sessions_active_user_idx ON bosun.sessions (user_id, phase) WHERE deleted_at IS NULL;

CREATE TABLE bosun.session_events (
    id          uuid PRIMARY KEY,
    session_id  uuid NOT NULL REFERENCES bosun.sessions (id) ON DELETE CASCADE,
    type        text NOT NULL,
    payload     jsonb NOT NULL DEFAULT '{}'::jsonb,
    occurred_at timestamptz NOT NULL
);

CREATE INDEX session_events_session_time_idx ON bosun.session_events (session_id, occurred_at);
