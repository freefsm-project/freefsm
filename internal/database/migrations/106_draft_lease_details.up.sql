ALTER TABLE leases ADD COLUMN starts_on date,
 ADD COLUMN ends_on date,
 ADD COLUMN premises_id bigint REFERENCES locations(id),
 ADD COLUMN agreement_file_id bigint REFERENCES files(id),
 ADD COLUMN details_revision bigint NOT NULL DEFAULT 0;

-- Intent is deliberately undated and never enters finalized coverage.
CREATE TABLE lease_intended_assets (
 lease_id bigint NOT NULL REFERENCES leases(id),
 asset_id bigint NOT NULL REFERENCES assets(id),
 PRIMARY KEY(lease_id,asset_id)
);
CREATE TRIGGER lease_intended_lock BEFORE INSERT OR UPDATE OR DELETE ON lease_intended_assets FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE FUNCTION lease_intended_selection_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM leases l JOIN assets a ON a.id=NEW.asset_id AND a.company_id=l.company_id
  WHERE l.id=NEW.lease_id AND l.deleted_at IS NULL AND a.ownership_class='company_owned' AND a.deleted_at IS NULL) THEN
  RAISE EXCEPTION 'New intended Asset requires active Company-owned Asset and Draft';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_intended_selection BEFORE INSERT OR UPDATE ON lease_intended_assets FOR EACH ROW EXECUTE FUNCTION lease_intended_selection_guard();

CREATE OR REPLACE FUNCTION lease_registration_dependency_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='leases' THEN target:=NEW.id; ELSE target:=NEW.lease_id; END IF;
 IF EXISTS(SELECT 1 FROM leases l WHERE l.id=target AND NOT EXISTS(
  SELECT 1 FROM lease_master_dependencies d WHERE d.lease_id=l.id AND d.company_id=l.company_id
   AND d.customer_id=l.customer_id AND d.lifecycle=l.lifecycle AND d.archived=(l.deleted_at IS NOT NULL)
   AND d.premises_id IS NOT DISTINCT FROM l.premises_id)) THEN
  RAISE EXCEPTION 'Lease agreement and dependency projection must commit together';
 END IF;
 RETURN NULL;
END $$;
