-- Conversion requires strict tenant ownership. Rows created after migration 042 may
-- still be null because these legacy columns remained nullable.
DO $$
DECLARE company_count bigint;
BEGIN
  SELECT count(*) INTO company_count FROM companies;
  IF company_count = 1 THEN
    UPDATE files SET company_id=(SELECT id FROM companies) WHERE company_id IS NULL;
    UPDATE tag_links SET company_id=(SELECT id FROM companies) WHERE company_id IS NULL;
    UPDATE comments SET company_id=(SELECT id FROM companies) WHERE company_id IS NULL;
    UPDATE activity_logs SET company_id=(SELECT id FROM companies) WHERE company_id IS NULL;
  ELSE
    UPDATE files r SET company_id=d.company_id FROM (
      SELECT 'customer' typ,id,company_id FROM customers UNION ALL SELECT 'project',id,company_id FROM projects UNION ALL
      SELECT 'job',id,company_id FROM jobs UNION ALL SELECT 'asset',id,company_id FROM assets UNION ALL
      SELECT 'estimate',id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices UNION ALL SELECT 'item',id,company_id FROM items
    ) d WHERE r.company_id IS NULL AND r.object_type=d.typ AND r.object_id=d.id;
    UPDATE tag_links r SET company_id=d.company_id FROM (
      SELECT 'customer' typ,id,company_id FROM customers UNION ALL SELECT 'project',id,company_id FROM projects UNION ALL SELECT 'job',id,company_id FROM jobs UNION ALL SELECT 'asset',id,company_id FROM assets UNION ALL SELECT 'estimate',id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices UNION ALL SELECT 'item',id,company_id FROM items
    ) d WHERE r.company_id IS NULL AND r.object_type=d.typ AND r.object_id=d.id;
    UPDATE comments r SET company_id=d.company_id FROM (
      SELECT 'customer' typ,id,company_id FROM customers UNION ALL SELECT 'project',id,company_id FROM projects UNION ALL SELECT 'job',id,company_id FROM jobs UNION ALL SELECT 'asset',id,company_id FROM assets UNION ALL SELECT 'estimate',id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices UNION ALL SELECT 'item',id,company_id FROM items
    ) d WHERE r.company_id IS NULL AND r.object_type=d.typ AND r.object_id=d.id;
    UPDATE activity_logs r SET company_id=d.company_id FROM (
      SELECT 'customer' typ,id,company_id FROM customers UNION ALL SELECT 'project',id,company_id FROM projects UNION ALL SELECT 'job',id,company_id FROM jobs UNION ALL SELECT 'asset',id,company_id FROM assets UNION ALL SELECT 'estimate',id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices UNION ALL SELECT 'item',id,company_id FROM items
    ) d WHERE r.company_id IS NULL AND r.object_type=d.typ AND r.object_id=d.id;
  END IF;
  IF EXISTS(SELECT 1 FROM files WHERE company_id IS NULL) OR
     EXISTS(SELECT 1 FROM tag_links WHERE company_id IS NULL) OR
     EXISTS(SELECT 1 FROM comments WHERE company_id IS NULL) OR
     EXISTS(SELECT 1 FROM activity_logs WHERE company_id IS NULL) THEN
    RAISE EXCEPTION 'conversion preflight: relation ownership remains ambiguous; assign company_id on files, tag_links, comments, and activity_logs';
  END IF;
  IF EXISTS(
    SELECT 1 FROM files r JOIN (SELECT 'estimate' typ,id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices) d
      ON d.typ=r.object_type AND d.id=r.object_id WHERE r.company_id IS DISTINCT FROM d.company_id
  ) OR EXISTS(
    SELECT 1 FROM tag_links r JOIN (SELECT 'estimate' typ,id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices) d
      ON d.typ=r.object_type AND d.id=r.object_id WHERE r.company_id IS DISTINCT FROM d.company_id
  ) OR EXISTS(
    SELECT 1 FROM comments r JOIN (SELECT 'estimate' typ,id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices) d
      ON d.typ=r.object_type AND d.id=r.object_id WHERE r.company_id IS DISTINCT FROM d.company_id
  ) OR EXISTS(
    SELECT 1 FROM activity_logs r JOIN (SELECT 'estimate' typ,id,company_id FROM estimates UNION ALL SELECT 'invoice',id,company_id FROM invoices) d
      ON d.typ=r.object_type AND d.id=r.object_id WHERE r.company_id IS DISTINCT FROM d.company_id
  ) THEN RAISE EXCEPTION 'conversion preflight: estimate/invoice relation company mismatch'; END IF;
  IF EXISTS(
    SELECT 1 FROM status_workflows w JOIN statuses s ON s.workflow_id=w.id
    WHERE s.company_id IS DISTINCT FROM w.company_id
  ) THEN RAISE EXCEPTION 'conversion preflight: status/workflow company mismatch'; END IF;
  IF EXISTS(
    SELECT 1 FROM status_workflows w LEFT JOIN statuses s ON s.workflow_id=w.id AND lower(s.name)='draft'
    WHERE w.object_type IN ('estimate','invoice') GROUP BY w.id,w.object_type HAVING count(s.id) <> 1
  ) THEN RAISE EXCEPTION 'conversion preflight: every Estimate and Invoice workflow must contain exactly one status named Draft; rename or create one before migration'; END IF;
END $$;

ALTER TABLE files ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE tag_links ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE comments ALTER COLUMN company_id SET NOT NULL;
ALTER TABLE activity_logs ALTER COLUMN company_id SET NOT NULL;

ALTER TABLE status_workflows ADD CONSTRAINT status_workflows_id_company_unique UNIQUE(id,company_id);
ALTER TABLE statuses ADD CONSTRAINT statuses_workflow_company_fk FOREIGN KEY(workflow_id,company_id) REFERENCES status_workflows(id,company_id);
ALTER TABLE statuses ADD COLUMN estimate_convertible BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE statuses ADD COLUMN document_role TEXT NOT NULL DEFAULT 'standard' CHECK(document_role IN ('standard','draft'));
UPDATE statuses s SET document_role='draft' FROM status_workflows w
 WHERE w.id=s.workflow_id AND w.object_type IN ('estimate','invoice') AND lower(s.name)='draft';
UPDATE statuses s SET estimate_convertible=true FROM status_workflows w
 WHERE w.id=s.workflow_id AND w.object_type='estimate' AND lower(s.name) IN ('accepted','approved');
CREATE UNIQUE INDEX statuses_one_document_role_per_workflow ON statuses(workflow_id) WHERE document_role='draft';
CREATE FUNCTION validate_status_conversion_capabilities() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE object_kind text;
BEGIN
  SELECT object_type INTO object_kind FROM status_workflows WHERE id=NEW.workflow_id AND company_id=NEW.company_id;
  IF NEW.estimate_convertible AND object_kind IS DISTINCT FROM 'estimate' THEN RAISE EXCEPTION 'only estimate statuses may be convertible'; END IF;
  IF NEW.document_role='draft' AND object_kind NOT IN ('estimate','invoice') THEN RAISE EXCEPTION 'only document statuses may have Draft role'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER status_conversion_capabilities BEFORE INSERT OR UPDATE ON statuses FOR EACH ROW EXECUTE FUNCTION validate_status_conversion_capabilities();

ALTER TABLE custom_field_definitions ADD COLUMN conversion_key TEXT;
ALTER TABLE custom_field_definitions ADD CONSTRAINT custom_field_conversion_key_valid
 CHECK(conversion_key IS NULL OR (object_type IN ('estimate','invoice') AND conversion_key=btrim(conversion_key) AND conversion_key<>''));
CREATE UNIQUE INDEX custom_field_conversion_key_unique
 ON custom_field_definitions(company_id,object_type,conversion_key) WHERE conversion_key IS NOT NULL;
CREATE FUNCTION validate_custom_field_conversion_pair() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE other custom_field_definitions%ROWTYPE;
BEGIN
  IF NEW.conversion_key IS NULL THEN RETURN NEW; END IF;
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.company_id::text || ':custom-field:' || NEW.conversion_key,0));
  SELECT * INTO other FROM custom_field_definitions
   WHERE company_id=NEW.company_id AND conversion_key=NEW.conversion_key
     AND object_type<>NEW.object_type AND object_type IN ('estimate','invoice') AND id<>NEW.id LIMIT 1;
  IF FOUND AND (other.field_type IS DISTINCT FROM NEW.field_type OR
      (NEW.field_type IN ('select','checkbox') AND other.options::jsonb IS DISTINCT FROM NEW.options::jsonb)) THEN
    RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='conversion key counterpart must use the same field type and compatible options';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER custom_field_conversion_pair_guard BEFORE INSERT OR UPDATE OF company_id,object_type,conversion_key,field_type,options
 ON custom_field_definitions FOR EACH ROW EXECUTE FUNCTION validate_custom_field_conversion_pair();

ALTER TABLE estimates ADD COLUMN conversion_hidden_at TIMESTAMPTZ;
ALTER TABLE invoices ADD COLUMN conversion_hidden_at TIMESTAMPTZ;
CREATE INDEX estimates_normal_idx ON estimates(company_id,created_at DESC) WHERE deleted_at IS NULL AND conversion_hidden_at IS NULL;
CREATE INDEX invoices_normal_idx ON invoices(company_id,created_at DESC) WHERE deleted_at IS NULL AND conversion_hidden_at IS NULL;
ALTER TABLE estimates ADD CONSTRAINT estimates_id_company_unique UNIQUE(id,company_id);
ALTER TABLE invoices ADD CONSTRAINT invoices_id_company_unique UNIQUE(id,company_id);

CREATE FUNCTION validate_document_status_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.status_id IS NOT NULL AND NOT EXISTS(
    SELECT 1 FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id AND w.company_id=s.company_id
    WHERE s.id=NEW.status_id AND s.company_id=NEW.company_id AND w.object_type=TG_ARGV[0]
  ) THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE=TG_ARGV[0] || ' status must belong to the same company and workflow'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER estimate_status_ownership BEFORE INSERT OR UPDATE OF company_id,status_id ON estimates
 FOR EACH ROW EXECUTE FUNCTION validate_document_status_ownership('estimate');
CREATE TRIGGER invoice_status_ownership BEFORE INSERT OR UPDATE OF company_id,status_id ON invoices
 FOR EACH ROW EXECUTE FUNCTION validate_document_status_ownership('invoice');

CREATE TABLE estimate_invoice_conversion_cycles (
  id UUID PRIMARY KEY, company_id BIGINT NOT NULL REFERENCES companies(id),
  estimate_id BIGINT NOT NULL, invoice_id BIGINT NOT NULL, invoice_number BIGINT NOT NULL,
  source_snapshot JSONB NOT NULL, converted_by BIGINT NOT NULL, converted_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  reverted_by BIGINT, reverted_at TIMESTAMPTZ,
  FOREIGN KEY(estimate_id,company_id) REFERENCES estimates(id,company_id),
  FOREIGN KEY(invoice_id,company_id) REFERENCES invoices(id,company_id),
  FOREIGN KEY(converted_by,company_id) REFERENCES users(id,company_id),
  FOREIGN KEY(reverted_by,company_id) REFERENCES users(id,company_id),
  CHECK((reverted_at IS NULL)=(reverted_by IS NULL)), UNIQUE(company_id,invoice_id), UNIQUE(company_id,invoice_number)
);
CREATE UNIQUE INDEX conversion_one_active_cycle_per_estimate ON estimate_invoice_conversion_cycles(company_id,estimate_id) WHERE reverted_at IS NULL;
CREATE INDEX conversion_cycles_estimate_timeline ON estimate_invoice_conversion_cycles(company_id,estimate_id,converted_at,id);

CREATE TABLE estimate_invoice_conversion_operations (
  company_id BIGINT NOT NULL REFERENCES companies(id), operation TEXT NOT NULL CHECK(operation IN ('convert','revert')),
  idempotency_key UUID NOT NULL, actor_id BIGINT NOT NULL, request_fingerprint TEXT NOT NULL, result JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(company_id,operation,idempotency_key),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id)
);

CREATE FUNCTION guard_estimate_conversion_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' AND OLD.conversion_hidden_at IS NOT NULL THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND OLD.conversion_hidden_at IS NOT NULL AND NEW.conversion_hidden_at IS NOT NULL
    THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND NEW.conversion_hidden_at IS DISTINCT FROM OLD.conversion_hidden_at THEN
    IF OLD.conversion_hidden_at IS NULL AND NOT EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c WHERE c.company_id=OLD.company_id AND c.estimate_id=OLD.id AND c.reverted_at IS NULL)
      THEN RAISE EXCEPTION 'estimate hide requires active conversion cycle'; END IF;
    IF NEW.conversion_hidden_at IS NULL AND (EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c WHERE c.company_id=OLD.company_id AND c.estimate_id=OLD.id AND c.reverted_at IS NULL) OR NOT EXISTS(
      SELECT 1 FROM estimate_invoice_conversion_cycles c JOIN invoices i ON i.id=c.invoice_id AND i.company_id=c.company_id
      WHERE c.company_id=OLD.company_id AND c.estimate_id=OLD.id AND c.reverted_at IS NOT NULL AND i.conversion_hidden_at IS NOT NULL
        AND c.id::text=current_setting('freefsm.conversion_revert_cycle',true)
        AND c.id=(SELECT latest.id FROM estimate_invoice_conversion_cycles latest WHERE latest.company_id=OLD.company_id AND latest.estimate_id=OLD.id ORDER BY latest.converted_at DESC,latest.id DESC LIMIT 1)
    ))
      THEN RAISE EXCEPTION 'estimate restore requires reverted conversion cycle'; END IF;
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE FUNCTION guard_invoice_conversion_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='DELETE' AND OLD.conversion_hidden_at IS NOT NULL THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND OLD.conversion_hidden_at IS NOT NULL THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND NEW.conversion_hidden_at IS DISTINCT FROM OLD.conversion_hidden_at AND
     NOT EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c WHERE c.company_id=OLD.company_id AND c.invoice_id=OLD.id AND c.reverted_at IS NOT NULL AND c.id::text=current_setting('freefsm.conversion_revert_cycle',true))
    THEN RAISE EXCEPTION 'invoice hide requires reverted conversion cycle'; END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER estimate_conversion_tombstone_guard BEFORE UPDATE OR DELETE ON estimates FOR EACH ROW EXECUTE FUNCTION guard_estimate_conversion_tombstone();
CREATE TRIGGER invoice_conversion_tombstone_guard BEFORE UPDATE OR DELETE ON invoices FOR EACH ROW EXECUTE FUNCTION guard_invoice_conversion_tombstone();

CREATE FUNCTION guard_conversion_cycle() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
   IF TG_OP='DELETE' OR OLD.id IS DISTINCT FROM NEW.id OR OLD.company_id IS DISTINCT FROM NEW.company_id OR OLD.estimate_id IS DISTINCT FROM NEW.estimate_id OR
     OLD.invoice_id IS DISTINCT FROM NEW.invoice_id OR OLD.invoice_number IS DISTINCT FROM NEW.invoice_number OR OLD.source_snapshot IS DISTINCT FROM NEW.source_snapshot OR
     OLD.converted_by IS DISTINCT FROM NEW.converted_by OR OLD.converted_at IS DISTINCT FROM NEW.converted_at OR OLD.reverted_at IS NOT NULL OR NEW.reverted_at IS NULL OR NEW.reverted_by IS NULL OR NEW.id::text IS DISTINCT FROM current_setting('freefsm.conversion_revert_cycle',true)
  THEN RAISE EXCEPTION 'conversion provenance is immutable'; END IF; RETURN NEW;
END $$;
CREATE TRIGGER conversion_cycle_immutable BEFORE UPDATE OR DELETE ON estimate_invoice_conversion_cycles FOR EACH ROW EXECUTE FUNCTION guard_conversion_cycle();

CREATE FUNCTION guard_hidden_invoice_settlement() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target_invoice bigint;
BEGIN
  target_invoice := CASE TG_TABLE_NAME WHEN 'invoice_payments' THEN NEW.invoice_id WHEN 'payment_invoice_allocations' THEN NEW.invoice_id WHEN 'credit_applications' THEN NEW.invoice_id ELSE NULL END;
  IF target_invoice IS NOT NULL AND EXISTS(SELECT 1 FROM invoices WHERE id=target_invoice AND conversion_hidden_at IS NOT NULL)
    THEN RAISE EXCEPTION 'hidden invoice cannot be settled'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER hidden_invoice_payment_guard BEFORE INSERT ON invoice_payments FOR EACH ROW EXECUTE FUNCTION guard_hidden_invoice_settlement();
CREATE TRIGGER hidden_invoice_allocation_guard BEFORE INSERT ON payment_invoice_allocations FOR EACH ROW EXECUTE FUNCTION guard_hidden_invoice_settlement();
CREATE TRIGGER hidden_invoice_application_guard BEFORE INSERT ON credit_applications FOR EACH ROW EXECUTE FUNCTION guard_hidden_invoice_settlement();
