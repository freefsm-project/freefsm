-- Queue-time evidence is unknowable for installed Deliveries. NULL prevents
-- obsolete mail from advancing a current document, without rewriting history.
ALTER TABLE document_deliveries ADD COLUMN expected_commercial_revision bigint;
CREATE FUNCTION protect_delivery_commercial_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.expected_commercial_revision IS DISTINCT FROM OLD.expected_commercial_revision THEN
  RAISE EXCEPTION 'Delivery commercial intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_delivery_commercial_intent BEFORE UPDATE OF expected_commercial_revision
 ON document_deliveries FOR EACH ROW EXECUTE FUNCTION protect_delivery_commercial_intent();
