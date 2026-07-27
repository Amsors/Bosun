ALTER TABLE bosun.sessions
    ADD COLUMN memory_request_bytes bigint NOT NULL DEFAULT 2147483648;

ALTER TABLE bosun.sessions
    ADD CONSTRAINT sessions_memory_request_range
        CHECK (memory_request_bytes BETWEEN 1073741824 AND 68719476736);
