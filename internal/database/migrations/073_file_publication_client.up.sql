-- Publication belongs to its originating client, not to a transfer attempt.
-- Browser publication remains user-scoped across browser sign-in; API clients
-- are the authenticated mobile session identity, never a caller-supplied label.
ALTER TABLE file_publications ADD COLUMN client_key text
    CHECK (client_key = 'web' OR client_key ~ '^api:[1-9][0-9]*$');

-- Recover only unambiguous recorded attribution. Do not infer a client from
-- whichever transfer/session happens to exist now, or rewrite historical audit.
WITH evidence AS (
    SELECT p.user_id,p.operation_id,
        min(CASE
            WHEN a.channel='web' THEN 'web'
            WHEN a.channel='api' AND a.detail->>'session_id' ~ '^[1-9][0-9]*$'
                THEN 'api:' || (a.detail->>'session_id')
        END) AS client_key
    FROM file_publications p JOIN authorization_audit a
      ON a.actor_id=p.user_id AND a.target_type='file' AND a.target_id=p.file_id
      AND a.action='file.upload' AND a.outcome='success'
      AND coalesce(a.detail->>'operation_id',a.detail->>'publication_id')=p.operation_id::text
    GROUP BY p.user_id,p.operation_id
    HAVING count(*)=1
)
UPDATE file_publications p SET client_key=e.client_key FROM evidence e
WHERE p.user_id=e.user_id AND p.operation_id=e.operation_id AND e.client_key IS NOT NULL;

-- NULL means origin was not established. Such evidence retains its File and
-- intent but cannot be claimed by a replaying client; manual review is required.
CREATE FUNCTION guard_file_publication_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'File publication evidence is immutable'; END $$;
CREATE TRIGGER file_publication_evidence_immutable BEFORE UPDATE OR DELETE ON file_publications
FOR EACH ROW EXECUTE FUNCTION guard_file_publication_evidence();
