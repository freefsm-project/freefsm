-- Recovery epoch is nonsensitive and is replaced, never restored unchanged, by
-- the restore protection transaction. Clients retain the epoch with pending work.
CREATE TABLE session_instance (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 epoch uuid NOT NULL DEFAULT gen_random_uuid()
);
INSERT INTO session_instance(singleton) VALUES(true);

-- Preserve the upgrade inventory as recorded, including questionable timing.
-- 052 backfilled last_used_at from created_at for older browser sessions; this
-- snapshot is recorded metadata, not evidence of an online authorization.
CREATE TABLE IF NOT EXISTS session_upgrade_inventory (
 session_id bigint PRIMARY KEY,user_id bigint,recorded_kind text,
 recorded_issue_time timestamptz,recorded_last_use_time timestamptz,
 recorded_expiry timestamptz,recorded_revocation timestamptz,disposition text NOT NULL
);
INSERT INTO session_upgrade_inventory
 SELECT id AS session_id,user_id,kind AS recorded_kind,created_at AS recorded_issue_time,
 last_used_at AS recorded_last_use_time,expires_at AS recorded_expiry,revoked_at AS recorded_revocation,
 CASE WHEN kind IS NULL OR kind NOT IN ('web','mobile') OR created_at IS NULL OR last_used_at IS NULL OR expires_at IS NULL
 OR last_used_at<created_at-interval '1 second' OR (kind='mobile' AND expires_at>last_used_at+interval '720 hours')
 OR (kind='web' AND expires_at>created_at+interval '168 hours 1 second')
 OR (kind='web' AND last_used_at=created_at AND EXISTS(
   SELECT 1 FROM schema_migrations m WHERE m.name='052_mobile_sessions' AND sessions.created_at<m.applied_at
 ))
 THEN 'review-required' ELSE 'retain-recorded-deadline' END AS disposition
 FROM sessions ON CONFLICT DO NOTHING;
COMMENT ON TABLE session_upgrade_inventory IS 'Recorded pre-v1 session facts; ambiguous timing requires explicit review and reauthentication, never fabricated activity or deadline extension.';
CREATE FUNCTION preserve_session_inventory() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'session upgrade inventory is immutable'; END $$;
CREATE TRIGGER preserve_session_inventory BEFORE UPDATE OR DELETE ON session_upgrade_inventory FOR EACH ROW EXECUTE FUNCTION preserve_session_inventory();
