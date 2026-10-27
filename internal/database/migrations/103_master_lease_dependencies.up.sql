-- Dependency projections, not Lease agreements or invented coverage history.
-- An installation with an unrecognized Lease store needs an explicit mapping
-- before enabling master archive; silently treating it as empty is unsafe.
DO $$ BEGIN
 IF to_regclass('leases') IS NOT NULL OR to_regclass('lease_assets') IS NOT NULL OR to_regclass('lease_coverage') IS NOT NULL THEN
  RAISE EXCEPTION 'Existing Lease storage requires explicit dependency mapping review before migration 103';
 END IF;
END $$;

CREATE TABLE lease_master_dependencies (
 lease_id bigint PRIMARY KEY CHECK (lease_id > 0),
 company_id bigint NOT NULL,
 customer_id bigint NOT NULL REFERENCES customers(id),
 premises_id bigint REFERENCES locations(id),
 lifecycle text NOT NULL CHECK (lifecycle IN ('draft','scheduled','active','month_to_month','terminated')),
 archived boolean NOT NULL DEFAULT false,
 CHECK (NOT archived OR lifecycle IN ('draft','terminated'))
);
CREATE TABLE lease_asset_dependencies (
 lease_id bigint NOT NULL REFERENCES lease_master_dependencies(lease_id),
 asset_id bigint NOT NULL REFERENCES assets(id),
 starts_on date NOT NULL,
 ends_on date,
 PRIMARY KEY(lease_id,asset_id,starts_on),
 CHECK (ends_on IS NULL OR ends_on >= starts_on)
);
CREATE INDEX lease_dependency_premises ON lease_master_dependencies(premises_id);
CREATE INDEX lease_dependency_asset ON lease_asset_dependencies(asset_id);

CREATE FUNCTION lease_dependency_snapshot(lease bigint) RETURNS jsonb LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT to_jsonb(l) || jsonb_build_object('coverage',coalesce((SELECT jsonb_agg(to_jsonb(d) ORDER BY d.asset_id,d.starts_on)
  FROM lease_asset_dependencies d WHERE d.lease_id=l.lease_id),'[]'::jsonb)) FROM lease_master_dependencies l WHERE l.lease_id=lease),'null'::jsonb)
$$;

CREATE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$
 SELECT (CURRENT_TIMESTAMP AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=company),'UTC'))::date
$$;
CREATE FUNCTION lease_asset_blocks_archive(asset bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id)
  WHERE d.asset_id=asset AND NOT l.archived AND l.lifecycle IN ('scheduled','active','month_to_month')
   AND (d.ends_on IS NULL OR d.ends_on >= lease_dependency_today(l.company_id)))
$$;
CREATE FUNCTION lease_premises_blocks_archive(location bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_master_dependencies WHERE premises_id=location AND NOT archived
  AND lifecycle IN ('draft','scheduled','active','month_to_month'))
$$;
CREATE FUNCTION lease_customer_blocks_archive(customer bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(SELECT 1 FROM lease_master_dependencies WHERE customer_id=customer AND NOT archived AND lifecycle<>'terminated')
$$;

CREATE FUNCTION lease_dependency_validate(lease bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE l lease_master_dependencies;
BEGIN
 SELECT * INTO l FROM lease_master_dependencies WHERE lease_id=lease;
 IF NOT FOUND THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM customers WHERE id=l.customer_id AND company_id=l.company_id) OR
  (l.premises_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM locations WHERE id=l.premises_id AND company_id=l.company_id AND object_type='customer' AND object_id=l.customer_id)) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease dependencies must belong to the same Customer';
 END IF;
 IF NOT l.archived AND l.lifecycle <> 'terminated' THEN
  IF NOT EXISTS(SELECT 1 FROM customers WHERE id=l.customer_id AND deleted_at IS NULL) OR
   (l.premises_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM locations WHERE id=l.premises_id AND deleted_at IS NULL)) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease registration/finalization requires active Customer and premises';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id
  WHERE d.lease_id=lease AND (a.company_id IS DISTINCT FROM l.company_id OR a.ownership_class <> 'company_owned')) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage requires Company-owned Assets';
 END IF;
 IF NOT l.archived AND l.lifecycle IN ('scheduled','active','month_to_month') AND EXISTS(
  SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id
  WHERE d.lease_id=lease AND (d.ends_on IS NULL OR d.ends_on >= lease_dependency_today(l.company_id))
   AND (a.deleted_at IS NOT NULL OR (a.customer_id IS NOT NULL AND a.customer_id<>l.customer_id))) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage requires active Assets without another Customer placement';
 END IF;
END $$;

CREATE FUNCTION lease_dependency_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Retain Lease dependency identity; explicitly terminate or archive';
 END IF;
 IF TG_TABLE_NAME='lease_master_dependencies' THEN
  IF TG_OP='UPDATE' AND (NEW.lease_id<>OLD.lease_id OR NEW.company_id<>OLD.company_id OR NEW.customer_id<>OLD.customer_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease dependency identity and Customer are immutable';
  END IF;
 ELSE
  IF TG_OP='INSERT' OR NEW.asset_id IS DISTINCT FROM OLD.asset_id OR NEW.lease_id IS DISTINCT FROM OLD.lease_id THEN
   IF EXISTS(SELECT 1 FROM lease_master_dependencies WHERE lease_id=NEW.lease_id AND NOT archived AND lifecycle='draft')
    AND EXISTS(SELECT 1 FROM assets WHERE id=NEW.asset_id AND deleted_at IS NOT NULL) THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='New Draft selection requires an active Asset';
   END IF;
  END IF;
 END IF;
 PERFORM lease_dependency_validate(NEW.lease_id);
 RETURN NEW;
END $$;
CREATE TRIGGER lease_master_dependency_lock BEFORE INSERT OR UPDATE OR DELETE ON lease_master_dependencies FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_asset_dependency_lock BEFORE INSERT OR UPDATE OR DELETE ON lease_asset_dependencies FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_master_dependency_guard AFTER INSERT OR UPDATE ON lease_master_dependencies FOR EACH ROW EXECUTE FUNCTION lease_dependency_guard();
CREATE TRIGGER lease_master_dependency_retain BEFORE DELETE ON lease_master_dependencies FOR EACH ROW EXECUTE FUNCTION lease_dependency_guard();
CREATE TRIGGER lease_asset_dependency_guard AFTER INSERT OR UPDATE ON lease_asset_dependencies FOR EACH ROW EXECUTE FUNCTION lease_dependency_guard();

-- Preserve the existing Job Contact guard, including its scalar compatibility
-- link when a legacy writer has removed only the many-to-many projection.
CREATE OR REPLACE FUNCTION job_contact_archive_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND EXISTS(
  SELECT 1 FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id WHERE
   (j.customer_contact_id=NEW.id OR EXISTS(SELECT 1 FROM job_contacts jc WHERE jc.job_id=j.id AND jc.contact_id=NEW.id))
   AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled')) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Contact is linked to unfinished Jobs';
 END IF;
 RETURN NEW;
END $$;
CREATE FUNCTION master_job_contact_selection_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 -- Compatibility mirroring can repeat an already recorded historical link.
 IF EXISTS(SELECT 1 FROM job_contacts WHERE job_id=NEW.job_id AND contact_id=NEW.contact_id) THEN RETURN NEW; END IF;
 IF NOT EXISTS(SELECT 1 FROM jobs j JOIN customer_contacts c ON c.id=NEW.contact_id AND c.customer_id=j.customer_id AND c.company_id=j.company_id
  JOIN customers owner ON owner.id=j.customer_id AND owner.deleted_at IS NULL
  WHERE j.id=NEW.job_id AND j.deleted_at IS NULL AND c.deleted_at IS NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active same-Customer Job Contact';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER master_job_contact_write_lock BEFORE INSERT OR UPDATE OR DELETE ON job_contacts FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER master_job_contact_selection BEFORE INSERT OR UPDATE ON job_contacts FOR EACH ROW EXECUTE FUNCTION master_job_contact_selection_guard();

-- Stable registration/finalization hook. Call in the SAME transaction as the
-- owning Lease effect, current authorization and immutable before/after audit.
-- Dates are supplied facts; no defaults/backfill derive historical coverage.
CREATE FUNCTION set_lease_master_dependencies(lease bigint, company bigint, customer bigint, premises bigint,
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
END $$;

CREATE FUNCTION master_archive_dependency_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE blocked boolean; owner bigint;
BEGIN
 IF TG_TABLE_NAME='customers' THEN
  IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND lease_customer_blocks_archive(NEW.id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Unarchived Lease blocks Customer archive';
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='assets' THEN
  IF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NOT NULL AND
   (NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.location_id IS DISTINCT FROM OLD.location_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Restore Asset before changing association';
  END IF;
  IF NEW.customer_id IS NOT NULL AND NEW.customer_id IS DISTINCT FROM OLD.customer_id AND EXISTS(
   SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id)
   WHERE d.asset_id=NEW.id AND NOT l.archived AND l.lifecycle IN ('scheduled','active','month_to_month')
    AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id)) AND l.customer_id<>NEW.customer_id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage blocks placement with another Customer';
  END IF;
  IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND lease_asset_blocks_archive(NEW.id) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Current or future Lease coverage blocks Asset archive';
  END IF;
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='customer_contacts' THEN owner:=NEW.customer_id;
 ELSE
  IF NEW.object_type<>'customer' THEN RETURN NEW; END IF;
  owner:=NEW.object_id;
 END IF;
 IF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL AND NOT EXISTS(
  SELECT 1 FROM customers WHERE id=owner AND company_id=NEW.company_id AND deleted_at IS NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Restore requires an active Customer';
 END IF;
 IF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL THEN NEW.is_primary:=false; END IF;
 IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL THEN
  IF TG_TABLE_NAME='locations' THEN
   SELECT lease_premises_blocks_archive(NEW.id) OR EXISTS(SELECT 1 FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id
    WHERE j.location_id=NEW.id AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled'))
    OR EXISTS(SELECT 1 FROM projects p LEFT JOIN statuses s ON s.id=p.status_id WHERE p.location_id=NEW.id
     AND p.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('project:completed','project:canceled')) INTO blocked;
   IF blocked THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Unfinished work or Lease premises blocks archive'; END IF;
  END IF;
  NEW.is_primary:=false;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER master_contact_archive_dependencies BEFORE UPDATE ON customer_contacts FOR EACH ROW EXECUTE FUNCTION master_archive_dependency_guard();
CREATE TRIGGER master_location_archive_dependencies BEFORE UPDATE ON locations FOR EACH ROW EXECUTE FUNCTION master_archive_dependency_guard();
CREATE TRIGGER master_asset_archive_dependencies BEFORE UPDATE ON assets FOR EACH ROW EXECUTE FUNCTION master_archive_dependency_guard();
CREATE TRIGGER master_customer_write_lock BEFORE INSERT OR UPDATE OR DELETE ON customers FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER master_customer_archive_dependencies BEFORE UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION master_archive_dependency_guard();
