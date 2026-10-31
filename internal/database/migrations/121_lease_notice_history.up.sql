-- Empty history only: installed termination and coverage facts are not inferred.
CREATE TABLE lease_notice_revisions (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 actor_id bigint NOT NULL REFERENCES users(id),
 operation_key text NOT NULL UNIQUE CHECK(length(operation_key) BETWEEN 16 AND 128),
 intent jsonb NOT NULL,
 action text NOT NULL CHECK(action IN ('create','revise','withdraw')),
 category text NOT NULL CHECK(category IN ('cancel_before_start','customer_ordinary','company_ordinary','customer_early','company_default','mutual_early','other_early')),
 initiator text NOT NULL CHECK(initiator IN ('customer','company','mutual','other')),
 notice_on date NOT NULL,
 effective_on date NOT NULL,
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 before_value jsonb NOT NULL,
 after_value jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX lease_notice_lease_revision ON lease_notice_revisions(lease_id,id DESC);
CREATE VIEW lease_notice_current AS SELECT DISTINCT ON(lease_id) * FROM lease_notice_revisions ORDER BY lease_id,id DESC;
CREATE TRIGGER lease_notice_immutable BEFORE UPDATE OR DELETE ON lease_notice_revisions FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_notice_lock BEFORE INSERT ON lease_notice_revisions FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

-- A future notice is not a lifecycle state. Every caller uses the company date.
CREATE FUNCTION lease_has_ended(target bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT action<>'withdraw' AND (category='cancel_before_start' OR effective_on<lease_dependency_today(l.company_id))
 FROM lease_notice_current n JOIN leases l ON l.id=n.lease_id WHERE n.lease_id=target),false)
 OR EXISTS(SELECT 1 FROM lease_master_dependencies WHERE lease_id=target AND lifecycle='terminated')
$$;

CREATE OR REPLACE FUNCTION lease_event_coverage_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint; commencement date; last_day date; state text; spans datemultirange; notice lease_notice_current;
BEGIN
 IF TG_OP='DELETE' THEN target:=OLD.lease_id; ELSE target:=NEW.lease_id; END IF;
 SELECT * INTO notice FROM lease_notice_current WHERE lease_id=target AND action<>'withdraw';
 IF NOT FOUND AND NOT EXISTS(SELECT 1 FROM lease_coverage_events WHERE lease_id=target) THEN RETURN NULL; END IF;
 IF notice.category='cancel_before_start' THEN
  IF EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=target) THEN RAISE EXCEPTION 'Cancelled Lease cannot commence coverage'; END IF;
  RETURN NULL;
 END IF;
 SELECT l.starts_on,d.lifecycle INTO commencement,state FROM leases l JOIN lease_master_dependencies d ON d.lease_id=l.id WHERE l.id=target;
 IF state='draft' THEN RAISE EXCEPTION 'Finalized coverage cannot become Draft'; END IF;
 last_day:=notice.effective_on;
 IF state='terminated' AND last_day IS NULL THEN SELECT max(ends_on) INTO last_day FROM lease_asset_dependencies WHERE lease_id=target; END IF;
 SELECT range_agg(daterange(starts_on,ends_on,'[]')) INTO spans FROM lease_asset_dependencies WHERE lease_id=target;
 IF spans IS DISTINCT FROM datemultirange(daterange(commencement,last_day,'[]')) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='Coverage must match the complete in-force Lease timeline';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_notice_coverage_complete AFTER INSERT ON lease_notice_revisions DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_event_coverage_validate();

CREATE OR REPLACE FUNCTION lease_customer_blocks_archive(customer bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_master_dependencies WHERE customer_id=customer AND NOT archived AND NOT lease_has_ended(lease_id))
$$;
CREATE OR REPLACE FUNCTION lease_premises_blocks_archive(location bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_master_dependencies WHERE premises_id=location AND NOT archived AND NOT lease_has_ended(lease_id))
$$;
CREATE OR REPLACE FUNCTION lease_asset_blocks_archive(asset bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l ON l.lease_id=d.lease_id
 WHERE d.asset_id=asset AND NOT l.archived AND l.lifecycle<>'draft' AND NOT lease_has_ended(l.lease_id)
 AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id)))
$$;

CREATE OR REPLACE FUNCTION lease_registration_dependency_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='leases' THEN target:=NEW.id; ELSE target:=NEW.lease_id; END IF;
 IF EXISTS(SELECT 1 FROM leases l WHERE l.id=target AND NOT EXISTS(
 SELECT 1 FROM lease_master_dependencies d WHERE d.lease_id=l.id AND d.company_id=l.company_id AND d.customer_id=l.customer_id
 AND (d.lifecycle=l.lifecycle OR (d.lifecycle='terminated' AND EXISTS(SELECT 1 FROM lease_notice_current n WHERE n.lease_id=l.id AND n.action<>'withdraw' AND (n.category='cancel_before_start' OR n.effective_on<lease_dependency_today(l.company_id)))))
 AND d.archived=(l.deleted_at IS NOT NULL) AND d.premises_id IS NOT DISTINCT FROM l.premises_id)) THEN
 RAISE EXCEPTION 'Lease agreement and dependency projection must commit together'; END IF;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION lease_finalized_draft_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='leases' THEN
  IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.id) AND (
   (NEW.starts_on,NEW.ends_on,NEW.premises_id,NEW.lifecycle) IS DISTINCT FROM (OLD.starts_on,OLD.ends_on,OLD.premises_id,OLD.lifecycle)
   OR (NEW.deleted_at IS DISTINCT FROM OLD.deleted_at AND NOT lease_has_ended(OLD.id))) THEN
   RAISE EXCEPTION 'Finalized agreement changes require a reasoned Lease revision'; END IF;
 ELSE
  IF TG_OP IN ('UPDATE','DELETE') AND EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.lease_id) THEN RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision'; END IF;
  IF TG_OP IN ('INSERT','UPDATE') AND EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=NEW.lease_id) THEN RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision'; END IF;
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
