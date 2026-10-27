-- Validate explicit facts, never reconstruct a Lease or rewrite old evidence.
-- Association periods are [starts_on, ends_before); coverage dates are inclusive.
-- NULL association start is the existing unknown-start observation, not a newly
-- invented acquisition date. Unplaced/empty periods do not conflict.
CREATE FUNCTION lease_asset_timeline_validate(asset bigint) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(
  SELECT 1 FROM lease_asset_dependencies d
  JOIN lease_master_dependencies l USING(lease_id)
  JOIN asset_association_periods p ON p.asset_id=d.asset_id
  WHERE d.asset_id=asset AND NOT l.archived
   AND l.lifecycle IN ('scheduled','active','month_to_month')
   AND p.customer_id IS NOT NULL AND p.customer_id<>l.customer_id
   AND daterange(p.starts_on,p.ends_before,'[)') && daterange(d.starts_on,d.ends_on,'[]')
 ) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage conflicts with a recorded Customer placement period';
 END IF;
END $$;

-- The domain commands call the same validator before writing audit. Deferred
-- guards also protect direct SQL and evaluate a multi-row replacement's final
-- timeline, rather than rejecting a valid intermediate repair ordering.
CREATE OR REPLACE FUNCTION lease_dependency_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Retain Lease dependency identity; explicitly terminate or archive';
 END IF;
 IF TG_TABLE_NAME='lease_master_dependencies' THEN
  IF TG_OP='UPDATE' AND (NEW.lease_id<>OLD.lease_id OR NEW.company_id<>OLD.company_id OR NEW.customer_id<>OLD.customer_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease dependency identity and Customer are immutable';
  END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION lease_timeline_dependency_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='lease_master_dependencies' THEN
  PERFORM lease_dependency_validate(NEW.lease_id);
  PERFORM lease_asset_timeline_validate(d.asset_id) FROM lease_asset_dependencies d WHERE d.lease_id=NEW.lease_id;
 ELSE
  IF TG_TABLE_NAME='lease_asset_dependencies' THEN
   IF TG_OP='DELETE' THEN
    PERFORM lease_dependency_validate(OLD.lease_id);
   ELSE
    PERFORM lease_dependency_validate(NEW.lease_id);
    IF TG_OP='INSERT' OR NEW.asset_id IS DISTINCT FROM OLD.asset_id OR NEW.lease_id IS DISTINCT FROM OLD.lease_id OR NEW.starts_on IS DISTINCT FROM OLD.starts_on THEN
     IF EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id) JOIN assets a ON a.id=d.asset_id
      WHERE d.lease_id=NEW.lease_id AND d.asset_id=NEW.asset_id AND d.starts_on=NEW.starts_on
       AND NOT l.archived AND l.lifecycle='draft' AND a.deleted_at IS NOT NULL) THEN
      RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='New Draft selection requires an active Asset';
     END IF;
    END IF;
   END IF;
  END IF;
  IF TG_OP='DELETE' THEN
   PERFORM lease_asset_timeline_validate(OLD.asset_id);
  ELSE
   PERFORM lease_asset_timeline_validate(NEW.asset_id);
   IF TG_OP='UPDATE' AND OLD.asset_id<>NEW.asset_id THEN PERFORM lease_asset_timeline_validate(OLD.asset_id); END IF;
  END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_master_timeline_guard AFTER INSERT OR UPDATE ON lease_master_dependencies
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_timeline_dependency_guard();
CREATE CONSTRAINT TRIGGER lease_asset_timeline_guard AFTER INSERT OR UPDATE OR DELETE ON lease_asset_dependencies
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_timeline_dependency_guard();
CREATE TRIGGER asset_event_dependency_lock BEFORE INSERT ON asset_association_events FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER asset_origin_revision_dependency_lock BEFORE INSERT ON asset_association_origin_revisions FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE CONSTRAINT TRIGGER asset_event_lease_timeline_guard AFTER INSERT ON asset_association_events
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_timeline_dependency_guard();
CREATE CONSTRAINT TRIGGER asset_origin_lease_timeline_guard AFTER INSERT ON asset_association_origin_revisions
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_timeline_dependency_guard();

CREATE OR REPLACE FUNCTION set_lease_master_dependencies(lease bigint, company bigint, customer bigint, premises bigint,
 state text, is_archived boolean, coverage jsonb) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(190019);
 IF jsonb_typeof(coverage) IS DISTINCT FROM 'array' THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Explicit Lease coverage array required';
 END IF;
 DELETE FROM lease_asset_dependencies d WHERE d.lease_id=lease AND NOT EXISTS(
  SELECT 1 FROM jsonb_to_recordset(coverage) AS x(asset_id bigint,starts_on date,ends_on date)
  WHERE x.asset_id=d.asset_id AND x.starts_on=d.starts_on);
 INSERT INTO lease_master_dependencies(lease_id,company_id,customer_id,premises_id,lifecycle,archived)
 VALUES(lease,company,customer,premises,state,is_archived)
 ON CONFLICT(lease_id) DO NOTHING;
 INSERT INTO lease_asset_dependencies(lease_id,asset_id,starts_on,ends_on)
 SELECT lease,x.asset_id,x.starts_on,x.ends_on FROM jsonb_to_recordset(coverage) AS x(asset_id bigint,starts_on date,ends_on date)
 ON CONFLICT(lease_id,asset_id,starts_on) DO UPDATE SET ends_on=excluded.ends_on;
 UPDATE lease_master_dependencies SET company_id=company,customer_id=customer,premises_id=premises,lifecycle=state,archived=is_archived WHERE lease_id=lease;
 PERFORM lease_dependency_validate(lease);
 PERFORM lease_asset_timeline_validate(d.asset_id) FROM lease_asset_dependencies d WHERE d.lease_id=lease;
END $$;

-- Archive eligibility is deliberately unchanged: an entirely past or Terminated
-- reference does not block master archive. Temporal consistency of edits to live
-- finalized coverage is separate and considers its whole recorded date range.
