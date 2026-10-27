-- A zero-total Draft projects Paid without becoming effectively Paid. Preserve
-- the existing settlement fence and guards, adding the missing Draft precedence
-- at the row commit boundary. No historical rows or grants are rewritten.
CREATE FUNCTION guard_invoice_archive_effective_status() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND
    EXISTS (SELECT 1 FROM statuses WHERE id=NEW.status_id AND category_key='invoice:draft') AND
    NOT invoice_has_active_settlement(OLD.id) THEN
   RAISE EXCEPTION 'invoice archive requires paid or void';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER invoice_archive_effective_status_guard BEFORE UPDATE OF deleted_at ON invoices
 FOR EACH ROW EXECUTE FUNCTION guard_invoice_archive_effective_status();
