ALTER TABLE custom_field_definitions ADD COLUMN retired boolean NOT NULL DEFAULT false;
ALTER TABLE custom_field_definitions ADD COLUMN retired_options jsonb NOT NULL DEFAULT '[]';
ALTER TABLE custom_field_definitions ADD COLUMN help_text text NOT NULL DEFAULT '';
ALTER TABLE custom_field_definitions ADD COLUMN include_pdf boolean NOT NULL DEFAULT false;
