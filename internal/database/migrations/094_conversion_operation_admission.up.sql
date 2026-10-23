-- Reserve recorded conversion UUIDs without inventing a historical client,
-- reviewed revision, intent, or winner. Original receipts remain authoritative.
-- admitted_at is this reservation's cutover time, not a reconstructed event.
WITH recorded AS (
 SELECT idempotency_key,CASE WHEN count(*)=1 THEN min(actor_id) END AS actor_id,
 CASE WHEN count(*)=1 THEN 'conversion.legacy' ELSE 'legacy.identity_conflict' END AS kind
 FROM estimate_invoice_conversion_operations GROUP BY idempotency_key
)
INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type)
 SELECT r.idempotency_key,d.id,r.actor_id,'legacy.unknown',r.kind
 FROM recorded r CROSS JOIN operation_deployment d
 ON CONFLICT(operation_id) DO NOTHING;
-- An existing cross-family binding is immutable. Neither family may reinterpret
-- it as new intent; read-only receipt recovery uses the original recorded facts.

CREATE FUNCTION guard_conversion_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR OLD.result IS NOT NULL
 OR NEW.company_id IS DISTINCT FROM OLD.company_id
 OR NEW.operation IS DISTINCT FROM OLD.operation
 OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key
 OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
 OR NEW.request_fingerprint IS DISTINCT FROM OLD.request_fingerprint
 OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.result IS NULL THEN
  RAISE EXCEPTION 'Conversion operation provenance is immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER conversion_operation_immutable BEFORE UPDATE OR DELETE
 ON estimate_invoice_conversion_operations FOR EACH ROW EXECUTE FUNCTION guard_conversion_operation();
