ALTER TABLE bosun.sessions
ADD COLUMN tier text NOT NULL DEFAULT 'small'
CHECK (tier IN ('small', 'medium'));
