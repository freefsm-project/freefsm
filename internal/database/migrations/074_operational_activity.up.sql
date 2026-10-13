-- This is an explicit, reviewed source mapping, not inferred historical
-- authorship. job.fields.edit already records immutable before/after facts and
-- has no legacy activity writer. Preserve that exact actor/time/source identity.
INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
SELECT j.company_id,CASE WHEN EXISTS(SELECT 1 FROM users u WHERE u.id=a.actor_id) THEN a.actor_id END,'job_fields_changed','job',a.target_id,
 jsonb_build_object('before',a.detail->'before','after',a.detail->'after','audit_id',a.id,'recorded_actor_id',a.actor_id),a.recorded_at,'authorization-audit:'||a.id
FROM authorization_audit a JOIN jobs j ON j.id=a.target_id
WHERE a.action='job.fields.edit' AND a.target_type='job' AND a.outcome='success';

CREATE FUNCTION operational_field_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.action='job.fields.edit' AND NEW.target_type='job' AND NEW.outcome='success' THEN
  INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
  SELECT company_id,NEW.actor_id,'job_fields_changed','job',NEW.target_id,
   jsonb_build_object('before',NEW.detail->'before','after',NEW.detail->'after','audit_id',NEW.id),NEW.recorded_at,'authorization-audit:'||NEW.id
  FROM jobs WHERE id=NEW.target_id;
 ELSIF NEW.target_type IN ('customer','contact','location','job','project','asset','estimate','invoice')
   AND NEW.action IN(NEW.target_type||'.create',NEW.target_type||'.edit',NEW.target_type||'.archive',NEW.target_type||'.restore')
   AND NEW.outcome='success' AND jsonb_typeof(NEW.detail->'after')='object' THEN
  INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
  VALUES((NEW.detail->'after'->>'company_id')::bigint,NEW.actor_id,
    CASE WHEN NEW.action=NEW.target_type||'.create' THEN 'created' WHEN NEW.action=NEW.target_type||'.archive' THEN 'archived' WHEN NEW.action=NEW.target_type||'.restore' THEN 'restored' ELSE 'updated' END,
    NEW.target_type,NEW.target_id,jsonb_build_object('before',NEW.detail->'before','after',NEW.detail->'after','audit_id',NEW.id),NEW.recorded_at,'authorization-audit:'||NEW.id);
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER operational_field_activity AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION operational_field_activity();

-- Ordinary history is append-only, like protected audit. No rewrite, purge,
-- anonymization, grant change or fabricated occurrence/actor is part of cutover.
CREATE FUNCTION operational_activity_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'operational activity is immutable'; END $$;
CREATE TRIGGER operational_activity_immutable BEFORE UPDATE OR DELETE ON activity_logs FOR EACH ROW EXECUTE FUNCTION operational_activity_immutable();
