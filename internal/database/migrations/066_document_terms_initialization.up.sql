-- Initialize new records only. NULL on an existing record remains an explicit
-- marker that historical terms were not recorded and require document review.
CREATE FUNCTION initialize_document_terms() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE kind text;
BEGIN
 IF NEW.terms IS NOT NULL OR NEW.company_id IS NULL THEN RETURN NEW; END IF;
 kind:=CASE TG_TABLE_NAME WHEN 'invoices' THEN 'invoice' ELSE 'estimate' END;
 SELECT coalesce(d.defaults->>(kind||'_terms'),
   CASE kind WHEN 'invoice' THEN cs.invoice_payment_terms ELSE cs.estimate_terms END)
 INTO NEW.terms FROM company_settings cs
 LEFT JOIN customer_document_defaults d ON d.customer_id=NEW.customer_id
 WHERE cs.company_id=NEW.company_id FOR UPDATE OF cs;
 IF NOT FOUND THEN RAISE EXCEPTION 'Company defaults unavailable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_terms_initialize BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION initialize_document_terms();
CREATE TRIGGER estimate_terms_initialize BEFORE INSERT ON estimates FOR EACH ROW EXECUTE FUNCTION initialize_document_terms();
