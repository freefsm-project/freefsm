-- A concurrency baseline, not reconstructed historical revisions. Existing
-- document facts, numbers, workflow, snapshots and provenance remain untouched.
ALTER TABLE estimates ADD COLUMN commercial_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE invoices ADD COLUMN commercial_revision bigint NOT NULL DEFAULT 1;

-- Repair only future allocation high-water marks from recorded numeric facts.
-- Unknown historical labels still require the existing Administrator review;
-- current prefixes are never substituted for unrecorded historical labels.
UPDATE company_settings s SET
 next_invoice_number=greatest(s.next_invoice_number,
  coalesce((SELECT max(invoice_number)+1 FROM invoices WHERE company_id=s.company_id),1),
  coalesce((SELECT max(number)+1 FROM issued_document_numbers WHERE company_id=s.company_id AND document_type='invoice'),1)),
 next_estimate_number=greatest(s.next_estimate_number,
  coalesce((SELECT max(document_number)+1 FROM estimates WHERE company_id=s.company_id),1),
  coalesce((SELECT max(number)+1 FROM issued_document_numbers WHERE company_id=s.company_id AND document_type='estimate'),1));

CREATE FUNCTION commercial_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_content jsonb; after_content jsonb;
BEGIN
 before_content := to_jsonb(OLD) - ARRAY['updated_at','commercial_revision'];
 after_content := to_jsonb(NEW) - ARRAY['updated_at','commercial_revision'];
 NEW.commercial_revision := OLD.commercial_revision;
 IF before_content IS DISTINCT FROM after_content THEN
  NEW.commercial_revision := OLD.commercial_revision + 1;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER zz_commercial_revision BEFORE UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION commercial_revision_guard();
CREATE TRIGGER zz_commercial_revision BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION commercial_revision_guard();
