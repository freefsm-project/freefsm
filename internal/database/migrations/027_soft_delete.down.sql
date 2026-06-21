DROP INDEX IF EXISTS idx_items_deleted;
DROP INDEX IF EXISTS idx_assets_deleted;
DROP INDEX IF EXISTS idx_invoices_deleted;
DROP INDEX IF EXISTS idx_estimates_deleted;
DROP INDEX IF EXISTS idx_projects_deleted;
DROP INDEX IF EXISTS idx_jobs_deleted;
DROP INDEX IF EXISTS idx_customers_deleted;

ALTER TABLE items DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE assets DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE invoices DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE estimates DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE projects DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE jobs DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE customers DROP COLUMN IF EXISTS deleted_at;
