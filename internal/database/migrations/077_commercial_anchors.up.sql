-- All existing writers participate before row locks, including document links
-- and permanent Service Entry blockers. No existing facts are rewritten.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM (
  SELECT company_id,customer_id,job_id,title FROM estimates WHERE conversion_hidden_at IS NULL
  UNION ALL SELECT company_id,customer_id,job_id,title FROM invoices WHERE conversion_hidden_at IS NULL
 ) d LEFT JOIN customers c ON c.id=d.customer_id
 WHERE job_title_is_blank(d.title) OR c.id IS NULL OR c.company_id IS DISTINCT FROM d.company_id
 OR (d.job_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.id=d.job_id AND j.company_id=d.company_id AND j.customer_id=d.customer_id))) THEN
  RAISE EXCEPTION 'Commercial cutover requires Administrator review: missing title/Customer or incompatible Job anchors; recorded document facts have not been changed';
 END IF;
END $$;

CREATE TRIGGER commercial_estimate_fence BEFORE INSERT OR UPDATE OR DELETE ON estimates FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER commercial_invoice_fence BEFORE INSERT OR UPDATE OR DELETE ON invoices FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER commercial_service_entry_fence BEFORE INSERT ON job_customer_blockers FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

CREATE OR REPLACE FUNCTION job_planning_invariants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND current_setting('freefsm.status_replacement',true)='allowed'
 AND (to_jsonb(NEW)-'status_id')=(to_jsonb(OLD)-'status_id')
 AND EXISTS(SELECT 1 FROM statuses a JOIN statuses b ON b.workflow_id=a.workflow_id AND b.category_key=a.category_key WHERE a.id=OLD.status_id AND b.id=NEW.status_id) THEN RETURN NEW; END IF;
 IF job_title_is_blank(NEW.job_type) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Job Title is required'; END IF;
 IF TG_OP='UPDATE' AND NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
  PERFORM id FROM estimates WHERE job_id=NEW.id AND conversion_hidden_at IS NULL ORDER BY id FOR UPDATE;
  PERFORM id FROM invoices WHERE job_id=NEW.id AND conversion_hidden_at IS NULL ORDER BY id FOR UPDATE;
  IF EXISTS(SELECT 1 FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.job_id=NEW.id AND i.conversion_hidden_at IS NULL AND
   (coalesce(s.category_key,'')<>'invoice:draft' OR i.deleted_at IS NOT NULL OR invoice_has_active_settlement(i.id)))
   OR EXISTS(SELECT 1 FROM job_customer_blockers WHERE job_id=NEW.id) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Unlink finalized or settled Invoices first; Service Entry provenance permanently blocks Job Customer replacement';
  END IF;
  IF NEW.project_id IS NOT NULL OR NEW.location_id IS NOT NULL OR NEW.customer_contact_id IS NOT NULL OR NEW.asset_id IS NOT NULL THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Job Customer replacement must clear operational links';
  END IF;
 END IF;
 IF NEW.project_id IS NOT NULL AND (TG_OP='INSERT' OR NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.customer_id IS DISTINCT FROM OLD.customer_id) THEN
  PERFORM 1 FROM projects WHERE id=NEW.project_id FOR UPDATE;
  IF NOT EXISTS(SELECT 1 FROM projects WHERE id=NEW.project_id AND customer_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Select an active Project belonging to the Customer';
  END IF;
 END IF;
 IF TG_OP='UPDATE' AND NEW.status_id IS DISTINCT FROM OLD.status_id AND NOT EXISTS(SELECT 1 FROM statuses WHERE id=NEW.status_id AND category_key IN ('job:completed','job:canceled')) THEN
  IF EXISTS(SELECT 1 FROM job_contacts jc JOIN customer_contacts c ON c.id=jc.contact_id WHERE jc.job_id=NEW.id AND (c.deleted_at IS NOT NULL OR c.customer_id<>NEW.customer_id)) THEN
   RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='master_contact_selection',MESSAGE='Select an active Contact belonging to the Customer';
  END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION reassign_job_commercial_anchors() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
  -- Conversion tombstones retain historical anchors; they are not operational
  -- Job links and are never silently rewritten as current documents.
  UPDATE estimates SET customer_id=NEW.customer_id,updated_at=clock_timestamp() WHERE job_id=NEW.id AND conversion_hidden_at IS NULL;
  UPDATE invoices SET customer_id=NEW.customer_id,updated_at=clock_timestamp() WHERE job_id=NEW.id AND conversion_hidden_at IS NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER reassign_job_commercial_anchors AFTER UPDATE OF customer_id ON jobs FOR EACH ROW EXECUTE FUNCTION reassign_job_commercial_anchors();

CREATE FUNCTION commercial_anchor_invariants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.job_id IS DISTINCT FROM OLD.job_id THEN
  IF NEW.customer_id IS NULL OR NOT EXISTS(SELECT 1 FROM customers WHERE id=NEW.customer_id AND company_id=NEW.company_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='A document requires its Customer';
  END IF;
  IF NEW.job_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM jobs WHERE id=NEW.job_id AND company_id=NEW.company_id AND customer_id=NEW.customer_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Select a same-Customer Job';
  END IF;
 END IF;
 IF TG_OP='INSERT' OR NEW.title IS DISTINCT FROM OLD.title THEN
  IF job_title_is_blank(NEW.title) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Document Title is required'; END IF;
 END IF;
 IF TG_TABLE_NAME='invoices' AND TG_OP='UPDATE' THEN
  IF (to_jsonb(OLD)->>'lease_id') IS NOT NULL AND NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Lease Invoice Customer is immutable';
  END IF;
  IF invoice_has_active_settlement(OLD.id) AND
   (to_jsonb(NEW)-ARRAY['updated_at','commercial_revision','settlement_state','deleted_at']) IS DISTINCT FROM
   (to_jsonb(OLD)-ARRAY['updated_at','commercial_revision','settlement_state','deleted_at']) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Active settlement locks commercial content and anchors';
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER commercial_anchor_invariants BEFORE INSERT OR UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION commercial_anchor_invariants();
CREATE TRIGGER commercial_anchor_invariants BEFORE INSERT OR UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION commercial_anchor_invariants();
