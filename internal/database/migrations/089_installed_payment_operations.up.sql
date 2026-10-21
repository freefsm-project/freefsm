-- Reserve actual installed UUIDs in the common registry. No historical client
-- is guessed. Original settlement receipts remain the source for reconciliation.
ALTER TABLE operation_admissions ALTER COLUMN user_id DROP NOT NULL;
WITH recorded AS (
 SELECT k.idempotency_key::uuid AS operation_id,p.actor_id
 FROM settlement_idempotency k
 JOIN invoice_payments p ON p.id=k.result_id AND p.company_id=k.company_id
 WHERE k.operation='record_payment'
 AND k.idempotency_key ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
), grouped AS (
 SELECT operation_id,CASE WHEN count(*)=1 THEN min(actor_id) END AS actor_id,
 CASE WHEN count(*)=1 THEN 'settlement.legacy' ELSE 'legacy.identity_conflict' END AS kind
 FROM recorded GROUP BY operation_id
)
INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type)
 SELECT g.operation_id,d.id,g.actor_id,'legacy.unknown',g.kind FROM grouped g CROSS JOIN operation_deployment d
 ON CONFLICT(operation_id) DO NOTHING;
-- Existing cross-family collisions stay reserved to their recorded binding;
-- reconciliation rejects ambiguity rather than rewriting either immutable fact.
