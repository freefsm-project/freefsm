ALTER TABLE document_pdf_versions ADD CONSTRAINT pdf_version_parent UNIQUE(id,company_id,document_type,document_id);
ALTER TABLE document_deliveries ADD COLUMN pdf_version_id bigint;
ALTER TABLE document_deliveries ADD CONSTRAINT delivery_selected_pdf_parent FOREIGN KEY(pdf_version_id,company_id,document_type,document_id) REFERENCES document_pdf_versions(id,company_id,document_type,document_id);
CREATE FUNCTION protect_delivery_pdf_selection() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.pdf_version_id IS DISTINCT FROM OLD.pdf_version_id THEN RAISE EXCEPTION 'Delivery PDF selection is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_delivery_pdf_selection BEFORE UPDATE OF pdf_version_id ON document_deliveries FOR EACH ROW EXECUTE FUNCTION protect_delivery_pdf_selection();
