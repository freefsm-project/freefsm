-- Stop before changing any recorded fact when the legacy contract is ambiguous.
-- Administrators must review/correct these source records explicitly first.
-- Match Go strings.TrimSpace / Unicode White_Space independently of database
-- locale. This is a predicate only: recorded titles are never normalized.
CREATE FUNCTION job_title_is_blank(value text) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT octet_length(btrim(coalesce(value,''),U&'\0009\000A\000B\000C\000D\0020\0085\00A0\1680\2000\2001\2002\2003\2004\2005\2006\2007\2008\2009\200A\2028\2029\202F\205F\3000'))=0;
$$;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM jobs j LEFT JOIN customers c ON c.id=j.customer_id
 WHERE job_title_is_blank(j.job_type) OR c.id IS NULL OR j.company_id IS DISTINCT FROM c.company_id
 OR (j.project_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM projects p WHERE p.id=j.project_id AND p.customer_id=j.customer_id AND p.company_id=j.company_id))
 OR (j.location_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM locations l WHERE l.id=j.location_id AND l.object_type='customer' AND l.object_id=j.customer_id AND l.company_id=j.company_id))
 OR (j.customer_contact_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM customer_contacts c WHERE c.id=j.customer_contact_id AND c.customer_id=j.customer_id AND c.company_id=j.company_id))
 OR (j.asset_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM assets a WHERE a.id=j.asset_id AND a.customer_id=j.customer_id AND a.company_id=j.company_id)
 AND coalesce(nullif(current_setting('freefsm.asset_association_review',true),'')::jsonb->>'administrator_reviewed','false')<>'true')) THEN
 RAISE EXCEPTION 'Job cutover requires Administrator review: missing title/Customer or ambiguous cross-Customer links; original records have not been changed';
 END IF;
END $$;

-- Permanent provenance anchors registered atomically by the Service Entry
-- owning integration. Removing or revising an entry cannot unlock its Job.
CREATE TABLE job_customer_blockers (
 job_id BIGINT NOT NULL REFERENCES jobs(id),
 service_entry_id BIGINT NOT NULL,
 PRIMARY KEY(job_id,service_entry_id)
);
CREATE FUNCTION job_customer_blocker_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Job Service Entry provenance is permanent'; END $$;
CREATE TRIGGER job_customer_blocker_immutable BEFORE UPDATE OR DELETE ON job_customer_blockers FOR EACH ROW EXECUTE FUNCTION job_customer_blocker_immutable();

CREATE TABLE job_contacts (
 job_id BIGINT NOT NULL REFERENCES jobs(id),
 contact_id BIGINT NOT NULL REFERENCES customer_contacts(id),
 PRIMARY KEY(job_id,contact_id)
);
-- This copies only an explicitly recorded relationship, with no invented actor
-- or historical acceptance time and no inferred Primary Customer record.
INSERT INTO job_contacts SELECT id,customer_contact_id FROM jobs WHERE customer_contact_id IS NOT NULL;

CREATE FUNCTION job_contact_mirror() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
   DELETE FROM job_contacts WHERE job_id=NEW.id;
  ELSIF NEW.customer_contact_id IS DISTINCT FROM OLD.customer_contact_id THEN
   DELETE FROM job_contacts WHERE job_id=NEW.id AND contact_id=OLD.customer_contact_id;
  END IF;
 END IF;
 IF NEW.customer_contact_id IS NOT NULL THEN
  INSERT INTO job_contacts VALUES(NEW.id,NEW.customer_contact_id) ON CONFLICT DO NOTHING;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_contact_mirror AFTER INSERT OR UPDATE OF customer_contact_id,customer_id ON jobs FOR EACH ROW EXECUTE FUNCTION job_contact_mirror();

CREATE FUNCTION job_planning_invariants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF TG_OP='UPDATE' AND current_setting('freefsm.status_replacement',true)='allowed'
	AND (to_jsonb(NEW)-'status_id')=(to_jsonb(OLD)-'status_id')
	AND EXISTS(SELECT 1 FROM statuses a JOIN statuses b ON b.workflow_id=a.workflow_id AND b.category_key=a.category_key WHERE a.id=OLD.status_id AND b.id=NEW.status_id) THEN RETURN NEW; END IF;
 IF job_title_is_blank(NEW.job_type) THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Job Title is required'; END IF;
 IF TG_OP='UPDATE' AND NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
  IF EXISTS(SELECT 1 FROM estimates WHERE job_id=NEW.id) OR EXISTS(SELECT 1 FROM invoices WHERE job_id=NEW.id) OR EXISTS(SELECT 1 FROM job_customer_blockers WHERE job_id=NEW.id) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Job Customer replacement is blocked by document dependencies';
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
CREATE TRIGGER job_planning_invariants BEFORE INSERT OR UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION job_planning_invariants();

CREATE FUNCTION job_contact_archive_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND EXISTS(
  SELECT 1 FROM job_contacts jc JOIN jobs j ON j.id=jc.job_id LEFT JOIN statuses s ON s.id=j.status_id
  WHERE jc.contact_id=NEW.id AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Contact is linked to unfinished Jobs';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_contact_archive_guard BEFORE UPDATE ON customer_contacts FOR EACH ROW EXECUTE FUNCTION job_contact_archive_guard();
