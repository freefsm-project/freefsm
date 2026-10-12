-- Unknown historical senders remain unknown. Never backfill from current settings.
ALTER TABLE document_deliveries ADD COLUMN sender_address text;
ALTER TABLE company_settings ADD COLUMN invoice_email_html text NOT NULL DEFAULT '';
ALTER TABLE company_settings ADD COLUMN estimate_email_html text NOT NULL DEFAULT '';
CREATE FUNCTION guard_delivery_sender() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.sender_address IS DISTINCT FROM NEW.sender_address THEN
  RAISE EXCEPTION 'delivery sender snapshot is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_sender_immutable BEFORE UPDATE ON document_deliveries
FOR EACH ROW EXECUTE FUNCTION guard_delivery_sender();
