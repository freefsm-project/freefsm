-- Prospective Lease configuration. No Lease history is invented or converted.
ALTER TABLE company_settings ADD COLUMN lease_prefix text NOT NULL DEFAULT 'LSE-';
ALTER TABLE company_settings ADD COLUMN next_lease_number bigint NOT NULL DEFAULT 1;

CREATE FUNCTION protect_issued_number() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Issued numbers cannot be changed or reused'; END $$;
CREATE TRIGGER protect_issued_number BEFORE UPDATE OR DELETE OR TRUNCATE ON issued_document_numbers FOR EACH STATEMENT EXECUTE FUNCTION protect_issued_number();
