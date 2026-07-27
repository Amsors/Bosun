ALTER TABLE bosun.sessions
    DROP CONSTRAINT sessions_memory_request_range,
    DROP COLUMN memory_request_bytes;
