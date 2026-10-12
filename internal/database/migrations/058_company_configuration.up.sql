-- Preserve existing settings and numbering verbatim. New defaults are prospective.
ALTER TABLE company_settings ADD COLUMN configuration_revision bigint NOT NULL DEFAULT 1;
ALTER TABLE company_settings ADD COLUMN default_invoice_notes text NOT NULL DEFAULT '';
ALTER TABLE company_settings ADD COLUMN default_estimate_notes text NOT NULL DEFAULT '';
ALTER TABLE company_settings ADD COLUMN estimate_terms text NOT NULL DEFAULT '';
ALTER TABLE company_settings ADD COLUMN default_line_description text NOT NULL DEFAULT '';
ALTER TABLE company_settings ADD COLUMN estimate_valid_days integer;

CREATE FUNCTION company_configuration_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  -- Issuing a number advances a runtime cursor, not a reviewed configuration.
  -- Management commands also change updated_at, and therefore advance this
  -- revision even when the only managed value is a sequence high-water mark.
  IF (to_jsonb(NEW) - ARRAY['configuration_revision','next_invoice_number','next_estimate_number','next_lease_number'])
     IS DISTINCT FROM
     (to_jsonb(OLD) - ARRAY['configuration_revision','next_invoice_number','next_estimate_number','next_lease_number']) THEN
    NEW.configuration_revision := OLD.configuration_revision + 1;
  ELSE
    NEW.configuration_revision := OLD.configuration_revision;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER company_configuration_revision BEFORE UPDATE ON company_settings
FOR EACH ROW EXECUTE FUNCTION company_configuration_revision();
