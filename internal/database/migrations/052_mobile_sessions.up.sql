ALTER TABLE sessions
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'web',
    ADD COLUMN last_used_at TIMESTAMPTZ,
    ADD COLUMN revoked_at TIMESTAMPTZ,
    ADD COLUMN device_name TEXT,
    ADD CONSTRAINT sessions_kind_check CHECK (kind IN ('web', 'mobile'));

UPDATE sessions SET last_used_at = created_at WHERE last_used_at IS NULL;

ALTER TABLE sessions
    ALTER COLUMN last_used_at SET DEFAULT NOW(),
    ALTER COLUMN last_used_at SET NOT NULL;
