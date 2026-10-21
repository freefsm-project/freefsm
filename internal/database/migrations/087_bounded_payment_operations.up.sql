-- Preserve installed financial originals; this records only new command receipts.
CREATE TABLE bounded_payment_operations (
 operation_id UUID PRIMARY KEY REFERENCES operation_admissions(operation_id),
 user_id BIGINT NOT NULL REFERENCES users(id),
 client_key TEXT NOT NULL,
 receipt JSONB NOT NULL,
 created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER bounded_payment_operations_immutable BEFORE UPDATE OR DELETE ON bounded_payment_operations
 FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
