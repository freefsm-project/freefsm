DROP TRIGGER IF EXISTS archived_commercial_readonly ON estimates;
DROP TRIGGER IF EXISTS archived_commercial_readonly ON invoices;
DROP FUNCTION IF EXISTS guard_archived_commercial_document();
