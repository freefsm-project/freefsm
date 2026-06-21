ALTER TABLE company_settings
    ADD COLUMN IF NOT EXISTS invoice_color TEXT DEFAULT '#1a56db',
    ADD COLUMN IF NOT EXISTS invoice_footer TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS invoice_logo_path TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS invoice_payment_terms TEXT DEFAULT 'Net 30';
