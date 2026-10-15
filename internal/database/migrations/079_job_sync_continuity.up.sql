-- Pre-repair continuity may contain an unrecorded delivered purge. Keep its
-- evidence and opaque identities, but require authoritative replacement once.
ALTER TABLE job_sync_snapshots ADD COLUMN invalidated boolean NOT NULL DEFAULT true;
ALTER TABLE job_sync_snapshots ALTER COLUMN invalidated SET DEFAULT false;

CREATE INDEX job_sync_snapshot_client_valid
    ON job_sync_snapshots(user_id, session_hash) WHERE NOT invalidated;
