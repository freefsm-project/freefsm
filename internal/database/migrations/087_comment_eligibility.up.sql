-- Current protocol eligibility, not historical lifecycle attribution. Keep this
-- separate from business rows: commercial guards compare whole-row JSON.
CREATE TABLE comment_parent_eligibility (
 object_type text NOT NULL CHECK (object_type IN ('job','estimate','invoice')),
 object_id bigint NOT NULL,
 revision uuid NOT NULL DEFAULT gen_random_uuid(),
 PRIMARY KEY(object_type,object_id)
);
INSERT INTO comment_parent_eligibility(object_type,object_id)
 SELECT 'job',id FROM jobs UNION ALL SELECT 'estimate',id FROM estimates
 UNION ALL SELECT 'invoice',id FROM invoices;

CREATE FUNCTION advance_comment_parent_eligibility() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF jsonb_build_array(to_jsonb(NEW)->'deleted_at',to_jsonb(NEW)->'customer_id',
                     to_jsonb(NEW)->'job_id',to_jsonb(NEW)->'conversion_hidden_at')
    IS DISTINCT FROM
    jsonb_build_array(to_jsonb(OLD)->'deleted_at',to_jsonb(OLD)->'customer_id',
                     to_jsonb(OLD)->'job_id',to_jsonb(OLD)->'conversion_hidden_at') THEN
  INSERT INTO comment_parent_eligibility(object_type,object_id,revision)
   VALUES(TG_ARGV[0],NEW.id,gen_random_uuid())
   ON CONFLICT(object_type,object_id) DO UPDATE SET revision=EXCLUDED.revision;
 END IF;
 RETURN NEW;
END $$;
-- UPDATE only. New current baselines are allocated by authorized readers/writers;
-- INSERT triggers would interfere with exact backup/restore COPY of metadata.
CREATE TRIGGER comment_parent_eligibility AFTER UPDATE ON jobs
 FOR EACH ROW EXECUTE FUNCTION advance_comment_parent_eligibility('job');
CREATE TRIGGER comment_parent_eligibility AFTER UPDATE ON estimates
 FOR EACH ROW EXECUTE FUNCTION advance_comment_parent_eligibility('estimate');
CREATE TRIGGER comment_parent_eligibility AFTER UPDATE ON invoices
 FOR EACH ROW EXECUTE FUNCTION advance_comment_parent_eligibility('invoice');
