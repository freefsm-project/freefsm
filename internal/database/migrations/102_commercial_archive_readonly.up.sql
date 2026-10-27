-- Protect historical commercial content even from indirect writers such as
-- Job Customer reassignment. Restore changes archive state, never content.
CREATE FUNCTION guard_archived_commercial_document() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.deleted_at IS NOT NULL THEN
  IF TG_OP='DELETE' THEN RAISE EXCEPTION 'archived commercial documents are read-only'; END IF;
  -- Preserve the established, audited same-category vocabulary replacement.
  -- The guard runs before revision triggers, as does the tombstone guard.
  IF current_setting('freefsm.status_replacement',true)='allowed'
   AND (to_jsonb(NEW)-'status_id')=(to_jsonb(OLD)-'status_id')
   AND EXISTS(SELECT 1 FROM statuses a JOIN statuses b ON b.workflow_id=a.workflow_id AND b.category_key=a.category_key WHERE a.id=OLD.status_id AND b.id=NEW.status_id)
  THEN RETURN NEW; END IF;
  IF (to_jsonb(NEW)-ARRAY['deleted_at','updated_at','commercial_revision']) IS DISTINCT FROM
     (to_jsonb(OLD)-ARRAY['deleted_at','updated_at','commercial_revision']) THEN
   RAISE EXCEPTION 'archived commercial documents are read-only';
  END IF;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER archived_commercial_readonly BEFORE UPDATE OR DELETE ON estimates
 FOR EACH ROW EXECUTE FUNCTION guard_archived_commercial_document();
CREATE TRIGGER archived_commercial_readonly BEFORE UPDATE OR DELETE ON invoices
 FOR EACH ROW EXECUTE FUNCTION guard_archived_commercial_document();
