DROP VIEW IF EXISTS invoice_effective_status;
DROP TRIGGER IF EXISTS invoice_status_update_guard ON invoices;
DROP TRIGGER IF EXISTS estimate_status_update_guard ON estimates;
DROP TRIGGER IF EXISTS project_status_update_guard ON projects;
DROP TRIGGER IF EXISTS job_status_update_guard ON jobs;
DROP FUNCTION IF EXISTS guard_record_status_update();
DROP VIEW IF EXISTS status_category_read;
DROP TRIGGER IF EXISTS project_status_ownership ON projects;
DROP TRIGGER IF EXISTS job_status_ownership ON jobs;
DROP TRIGGER IF EXISTS status_category_invariants ON statuses;
DROP FUNCTION IF EXISTS enforce_status_category_invariants();
DROP FUNCTION IF EXISTS check_status_category_invariant(bigint,text);
DROP TRIGGER IF EXISTS status_category_guard ON statuses;
DROP FUNCTION IF EXISTS validate_status_category();
DROP INDEX IF EXISTS statuses_category_default_unique;
DROP INDEX IF EXISTS statuses_category_order_unique;
DROP INDEX IF EXISTS statuses_name_workflow_ci_unique;
ALTER TABLE statuses DROP CONSTRAINT IF EXISTS statuses_id_company_unique;
ALTER TABLE statuses DROP CONSTRAINT IF EXISTS statuses_category_key_valid;
ALTER TABLE statuses DROP CONSTRAINT IF EXISTS statuses_category_order_positive;
ALTER TABLE statuses DROP CONSTRAINT IF EXISTS statuses_name_not_blank;
ALTER TABLE statuses ADD COLUMN estimate_convertible BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE statuses ADD COLUMN document_role TEXT NOT NULL DEFAULT 'standard';
UPDATE statuses SET document_role='draft' WHERE category_key IN ('estimate:draft','invoice:draft') AND is_category_default;
UPDATE statuses SET estimate_convertible=true WHERE category_key='estimate:accepted';
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
ALTER TABLE statuses DROP COLUMN is_category_default;
ALTER TABLE statuses DROP COLUMN category_order;
ALTER TABLE statuses DROP COLUMN category_key;

CREATE OR REPLACE FUNCTION guard_settled_invoice_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE status_name text;
BEGIN
  IF invoice_has_active_settlement(OLD.id) AND
     (NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate) THEN
    RAISE EXCEPTION 'settled invoice monetary fields and customer are locked';
  END IF;
  IF NEW.status_id IS DISTINCT FROM OLD.status_id THEN
    SELECT lower(name) INTO status_name FROM statuses WHERE id=NEW.status_id;
    IF status_name='void' AND invoice_has_active_settlement(OLD.id) THEN RAISE EXCEPTION 'void requires zero active settlement'; END IF;
  END IF;
  IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
    SELECT lower(name) INTO status_name FROM statuses WHERE id=NEW.status_id;
    IF coalesce(status_name,'')<>'void' AND NEW.settlement_state<>'paid' THEN RAISE EXCEPTION 'invoice archive requires paid or void'; END IF;
  END IF;
  IF NEW.settlement_state IS DISTINCT FROM OLD.settlement_state AND NOT (
       NOT invoice_has_active_settlement(OLD.id) AND
       (NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate)
     ) AND current_setting('freefsm.settlement_projection',true) IS DISTINCT FROM 'on' THEN
    RAISE EXCEPTION 'settlement_state is ledger-derived';
  END IF;
  RETURN NEW;
END $$;
