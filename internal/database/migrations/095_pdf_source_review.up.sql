-- Generation reviews track presentation ABA without treating mail, numbering,
-- or future-document defaults as changes to an existing PDF's source.
ALTER TABLE company_settings ADD COLUMN pdf_presentation_revision bigint NOT NULL DEFAULT 1;
CREATE FUNCTION pdf_presentation_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.business_name,NEW.address,NEW.city,NEW.state,NEW.zip,NEW.phone,NEW.email,
        NEW.tax_id,NEW.invoice_color,NEW.invoice_footer,NEW.invoice_logo_path,
        NEW.pdf_show_line_item_descriptions,NEW.timezone,NEW.date_format)
 IS DISTINCT FROM
    ROW(OLD.business_name,OLD.address,OLD.city,OLD.state,OLD.zip,OLD.phone,OLD.email,
        OLD.tax_id,OLD.invoice_color,OLD.invoice_footer,OLD.invoice_logo_path,
        OLD.pdf_show_line_item_descriptions,OLD.timezone,OLD.date_format) THEN
   NEW.pdf_presentation_revision := OLD.pdf_presentation_revision + 1;
 ELSE
   NEW.pdf_presentation_revision := OLD.pdf_presentation_revision;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER pdf_presentation_revision BEFORE UPDATE ON company_settings
 FOR EACH ROW EXECUTE FUNCTION pdf_presentation_revision();

CREATE TABLE pdf_operations (
 operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 receipt jsonb NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER protect_pdf_operation BEFORE UPDATE OR DELETE OR TRUNCATE ON pdf_operations
 FOR EACH STATEMENT EXECUTE FUNCTION protect_pdf_version();
