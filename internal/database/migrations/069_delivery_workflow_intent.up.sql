-- NULL is deliberately retained for historical deliveries: their queue-time
-- revision evidence is unknown and cannot be reconstructed from current rows.
ALTER TABLE document_deliveries
 ADD COLUMN expected_status_revision uuid,
 ADD COLUMN expected_meaning_revision uuid;

CREATE FUNCTION protect_delivery_workflow_intent() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.expected_status_revision IS DISTINCT FROM OLD.expected_status_revision
    OR NEW.expected_meaning_revision IS DISTINCT FROM OLD.expected_meaning_revision THEN
  RAISE EXCEPTION 'Delivery workflow intent is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER protect_delivery_workflow_intent
 BEFORE UPDATE OF expected_status_revision,expected_meaning_revision ON document_deliveries
 FOR EACH ROW EXECUTE FUNCTION protect_delivery_workflow_intent();
