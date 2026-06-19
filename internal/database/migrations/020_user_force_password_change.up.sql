ALTER TABLE users ADD COLUMN force_password_change BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN welcome_email_sent_at TIMESTAMPTZ;

ALTER TABLE company_settings ADD COLUMN password_min_length INT NOT NULL DEFAULT 8;
ALTER TABLE company_settings ADD COLUMN password_require_uppercase BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE company_settings ADD COLUMN password_require_lowercase BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE company_settings ADD COLUMN password_require_digit BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE company_settings ADD COLUMN password_require_special BOOLEAN NOT NULL DEFAULT true;
