ALTER TABLE bosun.sessions
    ADD COLUMN priority text NOT NULL DEFAULT 'normal';

-- Sessions created before priorities existed all used bosun-free.
UPDATE bosun.sessions
SET priority = 'low';

ALTER TABLE bosun.sessions
    ADD CONSTRAINT sessions_priority_value
        CHECK (priority IN ('low', 'normal', 'high'));

CREATE INDEX sessions_pending_priority_idx
    ON bosun.sessions (
        (CASE priority WHEN 'high' THEN 3 WHEN 'normal' THEN 2 ELSE 1 END) DESC,
        created_at
    )
    WHERE deleted_at IS NULL AND phase IN ('Pending', 'Provisioning', 'Restoring');
