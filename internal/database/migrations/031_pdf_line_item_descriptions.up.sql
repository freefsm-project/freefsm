ALTER TABLE company_settings
    ADD COLUMN IF NOT EXISTS pdf_show_line_item_descriptions BOOLEAN NOT NULL DEFAULT FALSE;
