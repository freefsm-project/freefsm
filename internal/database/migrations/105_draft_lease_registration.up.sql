-- Native agreements begin only with user registration. Existing dependency
-- projections are not evidence of agreement contents and are never backfilled.
CREATE TABLE leases (
 id bigserial PRIMARY KEY,
 company_id bigint NOT NULL REFERENCES companies(id),
 customer_id bigint NOT NULL REFERENCES customers(id),
 document_number bigint NOT NULL,
 number_label text NOT NULL,
 title text NOT NULL DEFAULT '',
 reference text NOT NULL DEFAULT '',
 execution_date date,
 lifecycle text NOT NULL DEFAULT 'draft' CHECK (lifecycle = 'draft'),
 custom_fields jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 deleted_at timestamptz,
 UNIQUE(company_id,document_number),
 UNIQUE(company_id,number_label)
);
-- Reserve existing projection identities without turning them into agreements.
SELECT setval(pg_get_serial_sequence('leases','id'), greatest(1,coalesce((SELECT max(lease_id)+1 FROM lease_master_dependencies),1)), false);

CREATE FUNCTION lease_registration_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE n bigint; prefix text;
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'Lease identity must be retained';
 END IF;
 IF TG_OP='UPDATE' THEN
  IF (NEW.id,NEW.company_id,NEW.customer_id,NEW.document_number,NEW.number_label)
    IS DISTINCT FROM (OLD.id,OLD.company_id,OLD.customer_id,OLD.document_number,OLD.number_label) THEN
   RAISE EXCEPTION 'Lease identity and Customer are immutable';
  END IF;
  RETURN NEW;
 END IF;
 IF EXISTS(SELECT 1 FROM lease_master_dependencies WHERE lease_id=NEW.id) THEN
  RAISE EXCEPTION 'Lease identity already belongs to an existing dependency projection';
 END IF;
 SELECT next_lease_number,lease_prefix INTO n,prefix FROM company_settings WHERE company_id=NEW.company_id FOR UPDATE;
 IF NOT FOUND OR n<1 OR prefix ~ '[[:cntrl:]]' THEN
  RAISE EXCEPTION 'Lease numbering configuration requires review';
 END IF;
 NEW.document_number:=n;
 NEW.number_label:=btrim(prefix)||lpad(n::text,greatest(5,length(n::text)),'0');
 INSERT INTO issued_document_numbers VALUES(NEW.company_id,'lease',n,NEW.number_label);
 UPDATE company_settings SET next_lease_number=n+1 WHERE company_id=NEW.company_id;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_registration_identity BEFORE INSERT OR UPDATE OR DELETE ON leases FOR EACH ROW EXECUTE FUNCTION lease_registration_identity();
CREATE TRIGGER lease_registration_lock BEFORE INSERT OR UPDATE OR DELETE ON leases FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

-- A native agreement cannot commit without its matching dependency projection.
CREATE FUNCTION lease_registration_dependency_check() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='leases' THEN target:=NEW.id; ELSE target:=NEW.lease_id; END IF;
 IF EXISTS(SELECT 1 FROM leases l WHERE l.id=target AND NOT EXISTS(
  SELECT 1 FROM lease_master_dependencies d WHERE d.lease_id=l.id AND d.company_id=l.company_id
   AND d.customer_id=l.customer_id AND d.lifecycle=l.lifecycle AND d.archived=(l.deleted_at IS NOT NULL))) THEN
  RAISE EXCEPTION 'Lease agreement and dependency projection must commit together';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_registration_dependencies AFTER INSERT OR UPDATE ON leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_registration_dependency_check();
CREATE CONSTRAINT TRIGGER lease_native_dependencies AFTER INSERT OR UPDATE ON lease_master_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_registration_dependency_check();
