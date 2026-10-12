-- NULL is No Due Date; every recorded pre-v1 offset is retained.
ALTER TABLE company_settings ALTER COLUMN default_due_days DROP NOT NULL;
ALTER TABLE company_settings ALTER COLUMN default_due_days DROP DEFAULT;

-- Only initialization of a pristine workspace replaces the old migration
-- seed's Net-30 offset with the v1 No Due Date default. An installed workspace,
-- configured company, nondefault offset, or recorded Customer is never reset.
CREATE FUNCTION initialize_company_document_defaults() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM users) AND NOT EXISTS(SELECT 1 FROM customers) THEN
   UPDATE company_settings SET default_due_days=NULL
   WHERE company_id=NEW.company_id AND business_name='' AND default_due_days=30;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER initialize_company_document_defaults BEFORE INSERT ON users
FOR EACH ROW EXECUTE FUNCTION initialize_company_document_defaults();
