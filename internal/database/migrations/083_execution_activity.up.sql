-- Publish only evidenced execution effects. Dedicated histories and protected
-- audit remain immutable sources; no baseline authors/times are invented.
CREATE FUNCTION publish_execution_activity(a authorization_audit) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
 event_action text;
 facts jsonb;
 task_action text;
BEGIN
 IF a.target_type <> 'job' OR a.outcome <> 'success' THEN RETURN; END IF;
 IF a.action = 'job.field_notes.edit' THEN
  event_action := 'field_notes_updated';
  facts := jsonb_build_object('before',a.detail->'before','after',a.detail->'after');
 ELSIF a.action IN ('job.subtask.manage','job.subtask.complete','job.subtask.reopen') THEN
  -- The dedicated history supplies the exact action (manage alone does not),
  -- stable identity and snapshots, including removed entries and batch order.
  SELECT h.action,jsonb_build_object('before',h.before_value,'after',h.after_value,'subtask_id',h.subtask_id)
   INTO task_action,facts FROM subtask_history h
   WHERE h.job_id=a.target_id AND h.actor_id=a.actor_id
    AND h.operation_id::text=a.detail->>'operation_id';
  event_action := CASE task_action WHEN 'add' THEN 'subtask_added'
   WHEN 'rename' THEN 'subtask_renamed' WHEN 'reorder' THEN 'subtasks_reordered'
   WHEN 'complete' THEN 'subtask_completed' WHEN 'reopen' THEN 'subtask_uncompleted'
   WHEN 'remove' THEN 'subtask_removed' END;
 ELSE RETURN;
 END IF;
 -- An unmatched historical audit is not evidence of an attributable effect.
 IF event_action IS NULL THEN RETURN; END IF;
 INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at,event_key)
 SELECT j.company_id,CASE WHEN EXISTS(SELECT 1 FROM users u WHERE u.id=a.actor_id) THEN a.actor_id END,
  event_action,'job',j.id,
  facts || jsonb_build_object('audit_id',a.id,'recorded_actor_id',a.actor_id,
   'actor_name',to_jsonb(a)->'actor_name','channel',a.channel,
   'source_occurred_at',coalesce(nullif(a.detail->'occurred_at','null'::jsonb),to_jsonb(a)->'occurred_at')),
  a.recorded_at,'authorization-audit:'||a.id
 FROM jobs j WHERE j.id=a.target_id
 ON CONFLICT (event_key) DO NOTHING;
END $$;

-- Reconcile already-installed attributable history with the same mapping used
-- for future writes. Exact source audit identities make this idempotent.
DO $$ DECLARE a authorization_audit; BEGIN
 FOR a IN SELECT * FROM authorization_audit WHERE target_type='job' AND outcome='success'
  AND action IN ('job.field_notes.edit','job.subtask.manage','job.subtask.complete','job.subtask.reopen')
  ORDER BY id
 LOOP PERFORM publish_execution_activity(a); END LOOP;
END $$;

CREATE FUNCTION execution_activity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM publish_execution_activity(NEW);
 RETURN NEW;
END $$;
CREATE TRIGGER execution_activity AFTER INSERT ON authorization_audit
 FOR EACH ROW WHEN (NEW.target_type='job' AND NEW.outcome='success'
  AND NEW.action IN ('job.field_notes.edit','job.subtask.manage','job.subtask.complete','job.subtask.reopen'))
 EXECUTE FUNCTION execution_activity();
