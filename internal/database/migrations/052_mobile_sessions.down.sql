LOCK TABLE sessions IN ACCESS EXCLUSIVE MODE;

DELETE FROM sessions WHERE kind = 'mobile';

ALTER TABLE sessions
    DROP CONSTRAINT IF EXISTS sessions_kind_check,
    DROP COLUMN IF EXISTS device_name,
    DROP COLUMN IF EXISTS revoked_at,
    DROP COLUMN IF EXISTS last_used_at,
    DROP COLUMN IF EXISTS kind;
