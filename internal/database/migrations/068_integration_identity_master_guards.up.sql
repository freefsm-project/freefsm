-- Compose historical-number review with the existing exact tombstone guards.
-- The command authorizes and audits this exception in the reservation transaction.
CREATE FUNCTION is_legacy_number_review(old_row jsonb, new_row jsonb, kind text)
RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT current_setting('freefsm.legacy_number_review',true)='allowed'
    AND old_row->>'number_label'=''
    AND btrim(new_row->>'number_label')<>''
    AND new_row->>'number_label' !~ '[[:cntrl:]]'
    AND CASE kind WHEN 'invoice' THEN
      old_row-'number_label'=new_row-'number_label'
    ELSE
      old_row->>'document_number' IS NULL
      AND old_row-ARRAY['number_label','document_number']=new_row-ARRAY['number_label','document_number']
    END
    AND EXISTS(SELECT 1 FROM issued_document_numbers n
      WHERE n.company_id=(new_row->>'company_id')::bigint AND n.document_type=kind
        AND n.number=(new_row->>CASE kind WHEN 'invoice' THEN 'invoice_number' ELSE 'document_number' END)::bigint
        AND n.label=new_row->>'number_label');
$$;

CREATE OR REPLACE FUNCTION guard_estimate_conversion_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND is_legacy_number_review(to_jsonb(OLD),to_jsonb(NEW),'estimate') THEN RETURN NEW; END IF;
  IF TG_OP='UPDATE' AND current_setting('freefsm.status_replacement',true)='allowed'
    AND (to_jsonb(NEW)-'status_id')=(to_jsonb(OLD)-'status_id')
    AND EXISTS(SELECT 1 FROM statuses a JOIN statuses b ON a.workflow_id=b.workflow_id AND a.category_key=b.category_key WHERE a.id=OLD.status_id AND b.id=NEW.status_id)
    THEN RETURN NEW; END IF;
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
    )) THEN RAISE EXCEPTION 'estimate restore requires reverted conversion cycle'; END IF;
  END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;

CREATE OR REPLACE FUNCTION guard_invoice_conversion_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP='UPDATE' AND is_legacy_number_review(to_jsonb(OLD),to_jsonb(NEW),'invoice') THEN RETURN NEW; END IF;
  IF TG_OP='DELETE' AND OLD.conversion_hidden_at IS NOT NULL THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND OLD.conversion_hidden_at IS NOT NULL THEN RAISE EXCEPTION 'conversion tombstone is immutable'; END IF;
  IF TG_OP='UPDATE' AND NEW.conversion_hidden_at IS DISTINCT FROM OLD.conversion_hidden_at AND
     NOT EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c WHERE c.company_id=OLD.company_id AND c.invoice_id=OLD.id AND c.reverted_at IS NOT NULL AND c.id::text=current_setting('freefsm.conversion_revert_cycle',true))
    THEN RAISE EXCEPTION 'invoice hide requires reverted conversion cycle'; END IF;
  RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;

-- Definition replacement may retain historical selections, but restoration,
-- reopening and changes to any other fact must still validate active masters.
-- Keep zz_status_revision late: these comparisons precede revision mutation.
CREATE OR REPLACE FUNCTION master_work_selection() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE revalidate boolean; old_contact bigint; new_contact bigint; old_location bigint; old_customer bigint;
BEGIN
  IF TG_OP='UPDATE' AND current_setting('freefsm.status_replacement',true)='allowed'
    AND NEW.status_id IS DISTINCT FROM OLD.status_id
    AND (to_jsonb(NEW)-'status_id')=(to_jsonb(OLD)-'status_id')
    AND EXISTS(SELECT 1 FROM statuses a JOIN statuses b ON a.workflow_id=b.workflow_id AND a.category_key=b.category_key
      JOIN status_workflows w ON w.id=a.workflow_id AND w.company_id=NEW.company_id
      WHERE a.id=OLD.status_id AND b.id=NEW.status_id AND a.company_id=NEW.company_id AND b.company_id=NEW.company_id
        AND w.object_type=CASE TG_TABLE_NAME WHEN 'jobs' THEN 'job' ELSE 'project' END)
    THEN RETURN NEW; END IF;
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
