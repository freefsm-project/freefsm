-- Nullable precision explicitly distinguishes unreviewed legacy number fields.
-- Defaults are prospective; no recorded parent value is rewritten here.
ALTER TABLE custom_field_definitions ADD COLUMN IF NOT EXISTS precision integer;
ALTER TABLE custom_field_definitions ADD CONSTRAINT custom_field_precision_range CHECK (precision BETWEEN 0 AND 18);
ALTER TABLE custom_field_definitions ADD COLUMN default_value text;
CREATE UNIQUE INDEX custom_field_normalized_name ON custom_field_definitions(object_type,lower(btrim(name)));

-- Include insert phantoms, precision changes and direct definition writers in
-- the same fence used by online authorized details transactions.
CREATE FUNCTION lock_custom_field_definitions() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(190019); RETURN NULL; END $$;
CREATE TRIGGER custom_field_definition_commit_fence BEFORE INSERT OR UPDATE OR DELETE ON custom_field_definitions
FOR EACH STATEMENT EXECUTE FUNCTION lock_custom_field_definitions();

CREATE OR REPLACE FUNCTION validate_custom_field_conversion_pair() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE other custom_field_definitions%ROWTYPE;
BEGIN
 IF NEW.conversion_key IS NULL THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(hashtextextended(NEW.company_id::text || ':custom-field:' || NEW.conversion_key,0));
 SELECT * INTO other FROM custom_field_definitions
 WHERE company_id=NEW.company_id AND conversion_key=NEW.conversion_key
 AND object_type<>NEW.object_type AND object_type IN ('estimate','invoice') AND id<>NEW.id LIMIT 1;
 IF FOUND AND (other.field_type IS DISTINCT FROM NEW.field_type OR
 (NEW.field_type='number' AND other.precision IS DISTINCT FROM NEW.precision) OR
 (NEW.field_type IN ('select','multiselect','checkbox') AND other.options::jsonb IS DISTINCT FROM NEW.options::jsonb)) THEN
 RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='conversion key counterpart must use the same type, precision and options';
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER custom_field_conversion_pair_guard ON custom_field_definitions;
CREATE TRIGGER custom_field_conversion_pair_guard BEFORE INSERT OR UPDATE OF company_id,object_type,conversion_key,field_type,options,precision
ON custom_field_definitions FOR EACH ROW EXECUTE FUNCTION validate_custom_field_conversion_pair();

CREATE FUNCTION guard_settled_invoice_custom_fields() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.custom_fields IS DISTINCT FROM OLD.custom_fields AND invoice_has_active_settlement(OLD.id) THEN
 RAISE EXCEPTION 'Invoice Custom Fields are locked by active settlement';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER settled_invoice_custom_fields BEFORE UPDATE OF custom_fields ON invoices
FOR EACH ROW EXECUTE FUNCTION guard_settled_invoice_custom_fields();
