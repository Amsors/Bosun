DROP INDEX IF EXISTS bosun.sessions_pending_priority_idx;

ALTER TABLE bosun.sessions
    DROP CONSTRAINT IF EXISTS sessions_priority_value,
    DROP COLUMN IF EXISTS priority;
