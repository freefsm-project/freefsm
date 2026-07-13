-- Migration 047 performs no data cleanup or ownership rewrites.
DROP TRIGGER IF EXISTS invoice_file_ownership_guard ON invoices;
DROP TRIGGER IF EXISTS estimate_file_ownership_guard ON estimates;
DROP TRIGGER IF EXISTS asset_file_ownership_guard ON assets;
DROP TRIGGER IF EXISTS job_file_ownership_guard ON jobs;
DROP TRIGGER IF EXISTS project_file_ownership_guard ON projects;
DROP TRIGGER IF EXISTS customer_file_ownership_guard ON customers;
DROP FUNCTION IF EXISTS guard_file_target_ownership();
DROP TRIGGER IF EXISTS file_target_ownership ON files;
DROP FUNCTION IF EXISTS validate_file_target_ownership();
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_uploader_company_fk;
ALTER TABLE files DROP CONSTRAINT IF EXISTS files_company_fk;
