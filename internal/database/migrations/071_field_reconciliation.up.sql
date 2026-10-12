-- Tokens describe the current state only; they do not invent past revisions.
CREATE TABLE job_field_revisions (
 job_id bigint NOT NULL REFERENCES jobs(id), definition_id bigint NOT NULL,
 revision uuid NOT NULL DEFAULT gen_random_uuid(), PRIMARY KEY(job_id,definition_id)
);
INSERT INTO job_field_revisions(job_id,definition_id)
SELECT j.id,(v->>'definition_id')::bigint FROM jobs j,jsonb_array_elements(j.custom_fields) v;

CREATE FUNCTION revise_job_fields() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO job_field_revisions(job_id,definition_id)
 SELECT NEW.id,coalesce(n.value->>'definition_id',o.value->>'definition_id')::bigint
 FROM jsonb_array_elements(NEW.custom_fields) n
 FULL JOIN jsonb_array_elements(CASE WHEN TG_OP='INSERT' THEN '[]'::jsonb ELSE OLD.custom_fields END) o
 ON n.value->>'definition_id'=o.value->>'definition_id'
 WHERE n.value IS DISTINCT FROM o.value
 ON CONFLICT(job_id,definition_id) DO UPDATE SET revision=gen_random_uuid();
 RETURN NEW;
END $$;
CREATE TRIGGER job_field_revision AFTER INSERT OR UPDATE OF custom_fields ON jobs FOR EACH ROW EXECUTE FUNCTION revise_job_fields();

CREATE TABLE job_field_operations (
 user_id bigint NOT NULL REFERENCES users(id), client_hash text NOT NULL,
 operation_id uuid NOT NULL, job_id bigint NOT NULL REFERENCES jobs(id),
 intent jsonb NOT NULL, outcome text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(user_id,client_hash,operation_id)
);
CREATE FUNCTION guard_job_field_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'field operation receipts are immutable'; END $$;
CREATE TRIGGER immutable_job_field_operations BEFORE UPDATE OR DELETE ON job_field_operations FOR EACH ROW EXECUTE FUNCTION guard_job_field_operation();
