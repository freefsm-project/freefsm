-- Additive: recorded identity, ownership, legacy CRM data and references remain.
DO $$ BEGIN
  IF EXISTS(SELECT 1 FROM locations WHERE object_type='customer' AND is_primary GROUP BY object_id HAVING count(*)>1) THEN
    RAISE EXCEPTION 'Customer master preflight: duplicate Primary Locations; run freefsm-master-preflight and explicitly resolve designation before retrying. No records have been merged or removed.';
  END IF;
END $$;
ALTER TABLE customer_contacts ADD COLUMN deleted_at timestamptz;
ALTER TABLE customer_contacts ADD COLUMN is_primary boolean NOT NULL DEFAULT false;
ALTER TABLE customer_contacts ADD COLUMN service_notes text NOT NULL DEFAULT '';
ALTER TABLE customer_contacts ADD COLUMN role_title text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN deleted_at timestamptz;
ALTER TABLE locations ADD COLUMN service_notes text NOT NULL DEFAULT '';
ALTER TABLE locations ADD COLUMN access_notes text NOT NULL DEFAULT '';

CREATE FUNCTION master_immutable_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_TABLE_NAME='customer_contacts' THEN
    IF NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.company_id IS DISTINCT FROM OLD.company_id THEN
      RAISE EXCEPTION 'Contact ownership is immutable; create a new record';
    END IF;
  ELSE
    IF NEW.object_type IS DISTINCT FROM OLD.object_type OR NEW.object_id IS DISTINCT FROM OLD.object_id OR NEW.company_id IS DISTINCT FROM OLD.company_id THEN
      RAISE EXCEPTION 'Location ownership is immutable; create a new record';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER contact_immutable_owner BEFORE UPDATE ON customer_contacts FOR EACH ROW EXECUTE FUNCTION master_immutable_owner();
CREATE TRIGGER location_immutable_owner BEFORE UPDATE ON locations FOR EACH ROW EXECUTE FUNCTION master_immutable_owner();

CREATE UNIQUE INDEX contact_one_primary ON customer_contacts(customer_id) WHERE is_primary AND deleted_at IS NULL;
-- Legacy duplicate Location primaries require explicit review, never an arbitrary winner.
-- Enforcement for new changes is installed below without rewriting legacy facts.
CREATE FUNCTION location_primary_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.object_type='customer' AND NEW.is_primary AND NEW.deleted_at IS NULL THEN
    PERFORM 1 FROM customers WHERE id=NEW.object_id FOR UPDATE;
    IF EXISTS(SELECT 1 FROM locations WHERE object_type='customer' AND object_id=NEW.object_id AND id<>NEW.id AND is_primary AND deleted_at IS NULL) THEN
      RAISE EXCEPTION 'Another Primary Location exists; explicitly review primary designation';
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER location_primary_guard BEFORE INSERT OR UPDATE ON locations FOR EACH ROW EXECUTE FUNCTION location_primary_guard();
CREATE UNIQUE INDEX location_one_primary ON locations(object_id) WHERE object_type='customer' AND is_primary AND deleted_at IS NULL;

-- Every ownership-sensitive work writer, including older code, participates in
-- the same serialization boundary before taking row locks. No history rewrite.
CREATE FUNCTION master_write_lock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(190019);
  RETURN NULL;
END $$;
CREATE TRIGGER master_job_write_lock BEFORE INSERT OR UPDATE OR DELETE ON jobs FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER master_project_write_lock BEFORE INSERT OR UPDATE OR DELETE ON projects FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER master_contact_write_lock BEFORE INSERT OR UPDATE OR DELETE ON customer_contacts FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER master_location_write_lock BEFORE INSERT OR UPDATE OR DELETE ON locations FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

CREATE FUNCTION master_work_selection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE revalidate boolean; old_contact bigint; new_contact bigint; old_location bigint; old_customer bigint;
BEGIN
  IF TG_OP='UPDATE' THEN
    old_location:=OLD.location_id; old_customer:=OLD.customer_id;
    revalidate:=OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL;
    IF NEW.status_id IS DISTINCT FROM OLD.status_id AND NOT EXISTS(SELECT 1 FROM statuses WHERE id=NEW.status_id AND category_key IN ('job:completed','job:canceled','project:completed','project:canceled')) THEN
      revalidate:=true;
    END IF;
    IF TG_TABLE_NAME='jobs' THEN old_contact:=OLD.customer_contact_id; END IF;
  ELSE revalidate:=true;
  END IF;
  PERFORM 1 FROM customers WHERE id=NEW.customer_id FOR UPDATE;
  IF NEW.deleted_at IS NULL AND (revalidate OR NEW.customer_id IS DISTINCT FROM old_customer) AND NOT EXISTS(SELECT 1 FROM customers WHERE id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL) THEN
    RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='master_customer_selection',MESSAGE='Select an active Customer';
  END IF;
  IF NEW.location_id IS NOT NULL AND (revalidate OR NEW.location_id IS DISTINCT FROM old_location OR NEW.customer_id IS DISTINCT FROM old_customer) THEN
    IF NOT EXISTS(SELECT 1 FROM locations WHERE id=NEW.location_id AND object_type='customer' AND object_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL) THEN
      -- Restoring finished work retains historical archived links.
      IF NOT (TG_OP='UPDATE' AND NEW.location_id IS NOT DISTINCT FROM old_location AND NEW.customer_id IS NOT DISTINCT FROM old_customer AND EXISTS(SELECT 1 FROM statuses WHERE id=NEW.status_id AND category_key IN ('job:completed','job:canceled','project:completed','project:canceled'))) THEN
        RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='master_location_selection',MESSAGE='Select an active Location belonging to the Customer';
      END IF;
    END IF;
  END IF;
  IF TG_TABLE_NAME='jobs' THEN
    new_contact:=NEW.customer_contact_id;
    IF new_contact IS NOT NULL AND (revalidate OR new_contact IS DISTINCT FROM old_contact OR NEW.customer_id IS DISTINCT FROM old_customer) THEN
      IF NOT EXISTS(SELECT 1 FROM customer_contacts WHERE id=new_contact AND customer_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL) THEN
        IF NOT (TG_OP='UPDATE' AND new_contact IS NOT DISTINCT FROM old_contact AND NEW.customer_id IS NOT DISTINCT FROM old_customer AND EXISTS(SELECT 1 FROM statuses WHERE id=NEW.status_id AND category_key IN ('job:completed','job:canceled'))) THEN
          RAISE EXCEPTION USING ERRCODE='23514',CONSTRAINT='master_contact_selection',MESSAGE='Select an active Contact belonging to the Customer';
        END IF;
      END IF;
    END IF;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER master_job_selection BEFORE INSERT OR UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION master_work_selection();
CREATE TRIGGER master_project_selection BEFORE INSERT OR UPDATE ON projects FOR EACH ROW EXECUTE FUNCTION master_work_selection();
