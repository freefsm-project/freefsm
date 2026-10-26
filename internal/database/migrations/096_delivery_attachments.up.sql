-- NULL means the legacy snapshot did not record extra File evidence. Do not
-- invent historical attachment inventories while preserving pending outbox work.
ALTER TABLE document_deliveries ADD COLUMN attachment_snapshots JSONB;
ALTER TABLE document_deliveries ADD COLUMN composition_snapshot JSONB;
CREATE FUNCTION guard_delivery_attachments() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.attachment_snapshots IS DISTINCT FROM NEW.attachment_snapshots OR OLD.composition_snapshot IS DISTINCT FROM NEW.composition_snapshot THEN
  RAISE EXCEPTION 'delivery attachment snapshot is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_attachments_immutable BEFORE UPDATE ON document_deliveries
 FOR EACH ROW EXECUTE FUNCTION guard_delivery_attachments();

CREATE TABLE delivery_operations (
 operation_id UUID PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 client_key TEXT NOT NULL,
 receipt JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TRIGGER delivery_operation_immutable BEFORE UPDATE OR DELETE ON delivery_operations
 FOR EACH ROW EXECUTE FUNCTION guard_document_delivery_event();
