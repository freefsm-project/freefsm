DROP TRIGGER IF EXISTS invoice_archive_effective_status_guard ON invoices;
DROP FUNCTION IF EXISTS guard_invoice_archive_effective_status();
