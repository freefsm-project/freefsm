-- Orphan supported-target links deleted by the up migration cannot be restored.
DROP TRIGGER IF EXISTS invoice_tag_ownership_guard ON invoices;
DROP TRIGGER IF EXISTS estimate_tag_ownership_guard ON estimates;
DROP TRIGGER IF EXISTS asset_tag_ownership_guard ON assets;
DROP TRIGGER IF EXISTS job_tag_ownership_guard ON jobs;
DROP TRIGGER IF EXISTS project_tag_ownership_guard ON projects;
DROP TRIGGER IF EXISTS customer_tag_ownership_guard ON customers;
DROP FUNCTION IF EXISTS guard_tagged_target_ownership();
DROP TRIGGER IF EXISTS tag_link_target_ownership ON tag_links;
DROP FUNCTION IF EXISTS validate_tag_link_target_ownership();
ALTER TABLE tag_links DROP CONSTRAINT IF EXISTS tag_links_tag_company_fk;
ALTER TABLE tag_links ADD CONSTRAINT tag_links_tag_id_fkey FOREIGN KEY(tag_id) REFERENCES tags(id) ON DELETE CASCADE;
ALTER TABLE tags DROP CONSTRAINT IF EXISTS tags_id_company_unique;
ALTER TABLE tag_links DROP CONSTRAINT IF EXISTS tag_links_company_fk;
ALTER TABLE tags DROP CONSTRAINT IF EXISTS tags_company_fk;
ALTER TABLE tags ALTER COLUMN company_id DROP NOT NULL;
