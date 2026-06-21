ALTER TABLE company_settings
    DROP COLUMN IF EXISTS invoice_payment_terms,
    DROP COLUMN IF EXISTS invoice_logo_path,
    DROP COLUMN IF EXISTS invoice_footer,
    DROP COLUMN IF EXISTS invoice_color;
