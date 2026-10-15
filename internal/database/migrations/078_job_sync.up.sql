-- Protocol metadata only: installed domain identities and grants are untouched.
CREATE TABLE job_sync_identities (
 resource_type text NOT NULL,
 installed_id bigint NOT NULL,
 opaque_id text NOT NULL UNIQUE,
 PRIMARY KEY(resource_type, installed_id)
);
CREATE TABLE job_sync_snapshots (
 id text PRIMARY KEY,
 user_id bigint NOT NULL,
 session_hash text NOT NULL,
 epoch text NOT NULL,
 scope text NOT NULL,
 page_size integer NOT NULL CHECK(page_size BETWEEN 1 AND 200),
 grants_hash text NOT NULL,
 baseline jsonb NOT NULL,
 changes jsonb NOT NULL,
 removed jsonb NOT NULL,
 watermark timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '30 days',
 previous_cursor text UNIQUE
);
CREATE TABLE job_sync_tokens (
 token text PRIMARY KEY,
 snapshot_id text NOT NULL REFERENCES job_sync_snapshots(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK(kind IN ('page','cursor')),
 position integer NOT NULL,
 UNIQUE(snapshot_id,kind,position)
);
CREATE INDEX job_sync_snapshot_expiry ON job_sync_snapshots(expires_at);
