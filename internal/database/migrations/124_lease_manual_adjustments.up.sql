-- No backfill or grant changes: manual applications are new, explicit facts.
CREATE TABLE lease_manual_adjustments (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 asset_id bigint NOT NULL REFERENCES assets(id),
 invoice_id bigint NOT NULL REFERENCES invoices(id),
 actor_id bigint NOT NULL REFERENCES users(id),
 kind text NOT NULL CHECK(kind IN ('abatement','fee','damages')),
 amount_cents bigint NOT NULL CHECK(amount_cents>0),
 starts_on date NOT NULL,
 ends_on date NOT NULL CHECK(ends_on>=starts_on),
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 operation_key text NOT NULL UNIQUE CHECK(length(operation_key) BETWEEN 16 AND 128),
 intent jsonb NOT NULL,
 before_value jsonb NOT NULL,
 after_value jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX lease_manual_adjustment_history ON lease_manual_adjustments(lease_id,id);
CREATE INDEX lease_manual_adjustment_invoice ON lease_manual_adjustments(invoice_id);
CREATE TRIGGER lease_manual_adjustment_immutable BEFORE UPDATE OR DELETE ON lease_manual_adjustments FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_manual_adjustment_lock BEFORE INSERT ON lease_manual_adjustments FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

-- A selected ordinary Invoice acquires immutable Customer provenance without
-- pretending to be a generated Lease billing obligation.
CREATE FUNCTION lease_adjustment_customer_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.customer_id IS DISTINCT FROM OLD.customer_id AND EXISTS(SELECT 1 FROM lease_manual_adjustments WHERE invoice_id=OLD.id) THEN
  RAISE EXCEPTION 'Lease adjustment Invoice Customer is immutable' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_adjustment_customer_guard BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION lease_adjustment_customer_guard();
