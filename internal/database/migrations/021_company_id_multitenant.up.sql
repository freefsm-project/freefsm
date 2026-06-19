-- Add company_id columns to all entity tables for future multi-tenant support
-- These columns are optional (nullable) in single-tenant mode.

ALTER TABLE users ADD COLUMN company_id BIGINT;
ALTER TABLE customers ADD COLUMN company_id BIGINT;
ALTER TABLE jobs ADD COLUMN company_id BIGINT;
ALTER TABLE invoices ADD COLUMN company_id BIGINT;
ALTER TABLE estimates ADD COLUMN company_id BIGINT;
ALTER TABLE items ADD COLUMN company_id BIGINT;
ALTER TABLE projects ADD COLUMN company_id BIGINT;
ALTER TABLE locations ADD COLUMN company_id BIGINT;
ALTER TABLE company_settings ADD COLUMN company_id BIGINT;
ALTER TABLE tags ADD COLUMN company_id BIGINT;
ALTER TABLE comments ADD COLUMN company_id BIGINT;
ALTER TABLE custom_field_definitions ADD COLUMN company_id BIGINT;
ALTER TABLE statuses ADD COLUMN company_id BIGINT;
ALTER TABLE status_workflows ADD COLUMN company_id BIGINT;
ALTER TABLE time_entries ADD COLUMN company_id BIGINT;
ALTER TABLE customer_contacts ADD COLUMN company_id BIGINT;
ALTER TABLE tag_links ADD COLUMN company_id BIGINT;
ALTER TABLE password_reset_tokens ADD COLUMN company_id BIGINT;

-- Create companies table for future multi-tenant use
CREATE TABLE IF NOT EXISTS companies (
    id BIGSERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    slug TEXT UNIQUE NOT NULL,
    hostname TEXT,
    is_active BOOLEAN DEFAULT true,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Index for hostname lookups (future subdomain resolution)
CREATE INDEX IF NOT EXISTS idx_companies_hostname ON companies(hostname);
CREATE INDEX IF NOT EXISTS idx_companies_slug ON companies(slug);

-- Add indexes for company_id on core tables (future query scoping)
CREATE INDEX IF NOT EXISTS idx_users_company_id ON users(company_id);
CREATE INDEX IF NOT EXISTS idx_customers_company_id ON customers(company_id);
CREATE INDEX IF NOT EXISTS idx_jobs_company_id ON jobs(company_id);
CREATE INDEX IF NOT EXISTS idx_invoices_company_id ON invoices(company_id);
CREATE INDEX IF NOT EXISTS idx_estimates_company_id ON estimates(company_id);
CREATE INDEX IF NOT EXISTS idx_items_company_id ON items(company_id);
CREATE INDEX IF NOT EXISTS idx_projects_company_id ON projects(company_id);
CREATE INDEX IF NOT EXISTS idx_locations_company_id ON locations(company_id);
CREATE INDEX IF NOT EXISTS idx_tags_company_id ON tags(company_id);
CREATE INDEX IF NOT EXISTS idx_comments_company_id ON comments(company_id);
CREATE INDEX IF NOT EXISTS idx_custom_field_definitions_company_id ON custom_field_definitions(company_id);
CREATE INDEX IF NOT EXISTS idx_statuses_company_id ON statuses(company_id);
CREATE INDEX IF NOT EXISTS idx_status_workflows_company_id ON status_workflows(company_id);
CREATE INDEX IF NOT EXISTS idx_time_entries_company_id ON time_entries(company_id);
CREATE INDEX IF NOT EXISTS idx_customer_contacts_company_id ON customer_contacts(company_id);
CREATE INDEX IF NOT EXISTS idx_tag_links_company_id ON tag_links(company_id);
CREATE INDEX IF NOT EXISTS idx_password_reset_tokens_company_id ON password_reset_tokens(company_id);
