ALTER TABLE bosun.sessions
    DROP CONSTRAINT IF EXISTS sessions_display_name_length,
    DROP COLUMN IF EXISTS display_name;

