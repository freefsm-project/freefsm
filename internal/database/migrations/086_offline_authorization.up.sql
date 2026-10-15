-- General disconnected access is independent of the Clock staging resource.
-- No domain facts or nonprotected grants are inferred for installed records.
ALTER TABLE company_settings ADD COLUMN offline_hours integer NOT NULL DEFAULT 24
    CHECK (offline_hours BETWEEN 0 AND 72);
CREATE TABLE offline_authorization_receipts (
    operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
    user_id bigint NOT NULL REFERENCES users(id),
    client_key text NOT NULL,
    receipt jsonb NOT NULL
);
CREATE TRIGGER offline_authorization_receipts_immutable BEFORE UPDATE OR DELETE ON offline_authorization_receipts
    FOR EACH ROW EXECUTE FUNCTION security_operation_receipt_immutable();
