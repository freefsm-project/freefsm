-- An operation belongs to this deployment, one actor/client and one intent.
-- Domain receipts remain authoritative for their own dispositions and effects.
CREATE TABLE operation_deployment (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid()
);
INSERT INTO operation_deployment DEFAULT VALUES;
CREATE TABLE operation_admissions (
 operation_id uuid PRIMARY KEY,
 deployment_id uuid NOT NULL REFERENCES operation_deployment(id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 operation_type text NOT NULL,
 intent jsonb,
 admitted_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE FUNCTION operation_admission_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Operation binding is immutable'; END $$;
CREATE TRIGGER operation_admission_immutable BEFORE UPDATE OR DELETE ON operation_admissions
 FOR EACH ROW EXECUTE FUNCTION operation_admission_immutable();
CREATE TRIGGER operation_deployment_immutable BEFORE UPDATE OR DELETE ON operation_deployment
 FOR EACH ROW EXECUTE FUNCTION operation_admission_immutable();

-- Reserve actual installed identities. NULL intent means the original domain
-- receipt must validate its original intent; it never authorizes a new effect.
-- Do not invent an ordering/winner for historical cross-domain collisions.
WITH old AS (
 SELECT o.operation_id,o.user_id,
 CASE WHEN s.kind='mobile' THEN 'api:'||o.client_hash WHEN s.kind='web' THEN 'web' ELSE 'legacy.unknown' END AS client_key,
 'job.fields' AS kind FROM job_field_operations o LEFT JOIN sessions s ON s.user_id=o.user_id AND s.token_hash=o.client_hash
 UNION ALL
 SELECT operation_id,user_id,'api:'||client_hash,'commercial.'||document_type FROM commercial_operations
 UNION ALL
 SELECT operation_id,user_id,client_key,'job.execution' FROM job_execution_receipts
 UNION ALL
 SELECT operation_id,user_id,client_key,'comment' FROM comment_operation_receipts
 UNION ALL
 SELECT operation_id,user_id,client_key,'clock' FROM time_clock_receipts
 UNION ALL
 SELECT operation_id,user_id,client_key,'job.subtask' FROM subtask_operation_receipts
 UNION ALL
 SELECT operation_id,user_id,'api:'||client_key,'file.mutation' FROM file_mutation_operations
 UNION ALL
 SELECT p.operation_id,p.user_id,
 CASE WHEN p.client_key='web' THEN 'web' ELSE 'api:'||coalesce(s.token_hash,'legacy.unknown') END,
 'file.publication' FROM file_publications p LEFT JOIN sessions s ON p.client_key='api:'||s.id::text AND p.user_id=s.user_id
), grouped AS (
 SELECT operation_id,min(user_id) AS user_id,min(client_key) AS client_key,
 CASE WHEN count(*)=1 THEN min(kind) ELSE 'legacy.identity_conflict' END AS kind
 FROM old GROUP BY operation_id
)
INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type)
 SELECT operation_id,d.id,user_id,CASE WHEN client_key LIKE 'web:%' OR client_key LIKE ':%' THEN 'web' ELSE client_key END,kind FROM grouped CROSS JOIN operation_deployment d;

CREATE TABLE file_transport_receipts (
 operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 transfer_id text NOT NULL,
 code text NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER file_transport_receipt_immutable BEFORE UPDATE OR DELETE ON file_transport_receipts
 FOR EACH ROW EXECUTE FUNCTION operation_admission_immutable();

CREATE TABLE file_command_receipts (
 operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 file_id bigint NOT NULL,
 code text NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER file_command_receipt_immutable BEFORE UPDATE OR DELETE ON file_command_receipts
 FOR EACH ROW EXECUTE FUNCTION operation_admission_immutable();
