-- Future explicit events carry identity independently of reduced facts and of
-- the permission authorizing the effect (e.g. Job Customer reassignment).
-- Preserve installed mappings and historical rows; do not invent past events.
DROP TRIGGER operational_field_activity ON authorization_audit;
CREATE TRIGGER operational_field_activity AFTER INSERT ON authorization_audit
FOR EACH ROW WHEN (NOT (NEW.detail ? 'operational_event'))
EXECUTE FUNCTION operational_field_activity();

CREATE FUNCTION operational_explicit_activity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 contract jsonb := NEW.detail->'operational_event';
 tenant bigint;
 parent_tenant bigint;
BEGIN
 IF NEW.outcome <> 'success' THEN RETURN NEW; END IF;
 IF jsonb_typeof(contract) IS DISTINCT FROM 'object'
    OR NEW.target_type NOT IN ('customer','project','asset','estimate','invoice')
    OR contract->>'target_type' IS DISTINCT FROM NEW.target_type
    OR (contract->>'target_id')::bigint IS DISTINCT FROM NEW.target_id
    OR coalesce(contract->>'action','') NOT IN ('created','updated')
    OR jsonb_typeof(NEW.detail->'after') IS DISTINCT FROM 'object' THEN
  RAISE EXCEPTION 'invalid operational event contract';
 END IF;
 tenant := (contract->>'company_id')::bigint;
 -- Target names come only from the closed catalog above, never request SQL.
 EXECUTE format('SELECT company_id FROM %I WHERE id=$1', NEW.target_type || 's')
 INTO parent_tenant USING NEW.target_id;
 IF tenant IS NULL OR parent_tenant IS NULL OR tenant <> parent_tenant THEN
  RAISE EXCEPTION 'operational event tenant does not match target';
 END IF;
 INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
 VALUES(tenant,NEW.actor_id,contract->>'action',NEW.target_type,NEW.target_id,
  jsonb_build_object('before',NEW.detail->'before','after',NEW.detail->'after',
   'audit_id',NEW.id,'actor_name',to_jsonb(NEW)->'actor_name','channel',NEW.channel,
   'source_occurred_at',to_jsonb(NEW)->'occurred_at'),
  NEW.recorded_at,'authorization-audit:'||NEW.id);
 RETURN NEW;
END $$;

CREATE TRIGGER operational_explicit_activity AFTER INSERT ON authorization_audit
FOR EACH ROW WHEN (NEW.detail ? 'operational_event')
EXECUTE FUNCTION operational_explicit_activity();
