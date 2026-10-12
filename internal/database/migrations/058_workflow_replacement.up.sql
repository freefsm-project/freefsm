-- Definition replacement is not a document transition. Permit only the exact
-- same-category reference rewrite; all other tombstone facts remain immutable.
CREATE OR REPLACE FUNCTION guard_estimate_conversion_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
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
