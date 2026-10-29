CREATE TABLE lease_billing_retry_receipts (
 operation_key text PRIMARY KEY CHECK(operation_key ~ '^[0-9a-f]{64}$'),
 obligation_id bigint NOT NULL REFERENCES lease_billing_obligations(id),
 invoice_id bigint NOT NULL REFERENCES lease_invoice_generations(invoice_id),
 actor_id bigint NOT NULL REFERENCES users(id),
 review_hash text NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER lease_retry_receipt_immutable BEFORE UPDATE OR DELETE ON lease_billing_retry_receipts FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
