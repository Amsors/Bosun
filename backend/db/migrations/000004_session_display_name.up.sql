ALTER TABLE bosun.sessions
    ADD COLUMN display_name text NOT NULL DEFAULT '未命名会话';

UPDATE bosun.sessions
SET display_name = '会话 ' || substring(id::text, 1, 8);

ALTER TABLE bosun.sessions
    ADD CONSTRAINT sessions_display_name_length
        CHECK (char_length(display_name) BETWEEN 1 AND 80);
