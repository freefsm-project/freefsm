-- Existing labels remain unresolved until an authorized review; no historical
-- label or sequence is reconstructed from current presentation defaults.
ALTER TABLE invoices ADD COLUMN number_label text NOT NULL DEFAULT '';
ALTER TABLE estimates ADD COLUMN number_label text NOT NULL DEFAULT '';
ALTER TABLE estimates ADD COLUMN document_number bigint;
ALTER TABLE company_settings ADD COLUMN next_estimate_number bigint NOT NULL DEFAULT 1;
CREATE TABLE issued_document_numbers (
 company_id bigint NOT NULL REFERENCES companies(id),
 document_type text NOT NULL CHECK (document_type IN ('invoice','estimate','lease')),
 number bigint NOT NULL CHECK (number>0),
 label text NOT NULL,
 PRIMARY KEY(company_id,document_type,number),
 UNIQUE(company_id,document_type,label)
);
CREATE FUNCTION reserve_document_identity() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE next_number bigint; prefix text; kind text;
BEGIN
 kind := CASE TG_TABLE_NAME WHEN 'invoices' THEN 'invoice' ELSE 'estimate' END;
 IF TG_OP='UPDATE' THEN
   IF OLD.number_label<>'' AND NEW.number_label IS DISTINCT FROM OLD.number_label THEN RAISE EXCEPTION 'Document identity is immutable'; END IF;
   IF kind='invoice' THEN
     IF NEW.invoice_number IS DISTINCT FROM OLD.invoice_number THEN RAISE EXCEPTION 'Invoice number is immutable'; END IF;
   ELSE
     IF OLD.document_number IS NOT NULL AND NEW.document_number IS DISTINCT FROM OLD.document_number THEN RAISE EXCEPTION 'Estimate number is immutable'; END IF;
   END IF;
   RETURN NEW;
 END IF;
 IF NEW.company_id IS NULL THEN RETURN NEW; END IF;
 IF kind='invoice' THEN
   SELECT next_invoice_number,invoice_prefix INTO next_number,prefix FROM company_settings WHERE company_id=NEW.company_id FOR UPDATE;
   IF NOT FOUND THEN RAISE EXCEPTION 'Company settings unavailable'; END IF;
   IF EXISTS(SELECT 1 FROM invoices WHERE company_id=NEW.company_id AND number_label='') THEN RAISE EXCEPTION 'Legacy numbering requires review'; END IF;
   IF NEW.invoice_number IS NULL OR NEW.invoice_number=0 THEN NEW.invoice_number:=next_number; END IF;
   IF NEW.invoice_number<next_number THEN RAISE EXCEPTION 'Issued numbering high-water mark cannot be reused'; END IF;
   next_number:=NEW.invoice_number;
   IF prefix ~ '[[:cntrl:]]' THEN RAISE EXCEPTION 'Number prefix requires review'; END IF;
   NEW.number_label:=btrim(prefix)||lpad(next_number::text,greatest(5,length(next_number::text)),'0');
   UPDATE company_settings SET next_invoice_number=next_number+1 WHERE company_id=NEW.company_id;
 ELSE
   SELECT next_estimate_number,estimate_prefix INTO next_number,prefix FROM company_settings WHERE company_id=NEW.company_id FOR UPDATE;
   IF NOT FOUND THEN RAISE EXCEPTION 'Company settings unavailable'; END IF;
   IF EXISTS(SELECT 1 FROM estimates WHERE company_id=NEW.company_id AND number_label='') THEN RAISE EXCEPTION 'Legacy numbering requires review'; END IF;
   NEW.document_number:=next_number;
   IF prefix ~ '[[:cntrl:]]' THEN RAISE EXCEPTION 'Number prefix requires review'; END IF;
   NEW.number_label:=btrim(prefix)||lpad(next_number::text,greatest(5,length(next_number::text)),'0');
   UPDATE company_settings SET next_estimate_number=next_number+1 WHERE company_id=NEW.company_id;
 END IF;
 INSERT INTO issued_document_numbers VALUES(NEW.company_id,kind,next_number,NEW.number_label);
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_identity BEFORE INSERT OR UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION reserve_document_identity();
CREATE TRIGGER estimate_identity BEFORE INSERT OR UPDATE ON estimates FOR EACH ROW EXECUTE FUNCTION reserve_document_identity();
