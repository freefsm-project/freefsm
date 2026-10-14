-- Independent issuance/revocation outcomes, not Time Entry or pairing receipts.
-- Existing leases/sessions have no invented operation or client attribution.
CREATE TABLE clock_lease_receipts (
 operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 receipt jsonb NOT NULL
);
CREATE TABLE session_revocation_receipts (
 operation_id uuid PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id bigint NOT NULL REFERENCES users(id),
 client_key text NOT NULL,
 receipt jsonb NOT NULL
);
CREATE FUNCTION security_operation_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'security operation receipts are immutable'; END $$;
CREATE TRIGGER clock_lease_receipt_immutable BEFORE UPDATE OR DELETE ON clock_lease_receipts FOR EACH ROW EXECUTE FUNCTION security_operation_receipt_immutable();
CREATE TRIGGER session_revocation_receipt_immutable BEFORE UPDATE OR DELETE ON session_revocation_receipts FOR EACH ROW EXECUTE FUNCTION security_operation_receipt_immutable();
