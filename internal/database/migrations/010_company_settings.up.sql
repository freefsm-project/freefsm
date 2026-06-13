CREATE TABLE company_settings (
    id BIGSERIAL PRIMARY KEY,
    business_name TEXT NOT NULL DEFAULT '',
    address TEXT NOT NULL DEFAULT '',
    city TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT '',
    zip TEXT NOT NULL DEFAULT '',
    phone TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    tax_id TEXT NOT NULL DEFAULT '',
    default_tax_rate TEXT NOT NULL DEFAULT '0',
    invoice_prefix TEXT NOT NULL DEFAULT 'INV-',
    estimate_prefix TEXT NOT NULL DEFAULT 'EST-',
    default_due_days INT NOT NULL DEFAULT 30,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO company_settings DEFAULT VALUES;
