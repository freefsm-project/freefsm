-- Every existing generation remains the root of its period's immutable chain.
ALTER TABLE lease_invoice_generations DROP CONSTRAINT lease_invoice_generations_pkey;
ALTER TABLE lease_invoice_generations ADD PRIMARY KEY(invoice_id);
ALTER TABLE lease_invoice_generations ADD COLUMN predecessor_invoice_id bigint UNIQUE REFERENCES lease_invoice_generations(invoice_id);
ALTER TABLE lease_invoice_generations ADD COLUMN operation_key text UNIQUE;
ALTER TABLE lease_invoice_generations ADD COLUMN review_hash text;
ALTER TABLE lease_invoice_generations ADD COLUMN actor_id bigint REFERENCES users(id);
ALTER TABLE lease_invoice_generations ADD CONSTRAINT lease_generation_not_self CHECK(predecessor_invoice_id<>invoice_id);
ALTER TABLE lease_invoice_generations ADD CONSTRAINT lease_replacement_intent CHECK (
 (predecessor_invoice_id IS NULL AND operation_key IS NULL AND review_hash IS NULL AND actor_id IS NULL) OR
 (predecessor_invoice_id IS NOT NULL AND operation_key ~ '^[0-9a-f]{64}$' AND review_hash ~ '^[0-9a-f]{64}$' AND actor_id IS NOT NULL)
);
CREATE UNIQUE INDEX lease_generation_root ON lease_invoice_generations(obligation_id) WHERE predecessor_invoice_id IS NULL;
CREATE TRIGGER lease_generation_fence BEFORE INSERT ON lease_invoice_generations FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

CREATE FUNCTION lease_generation_chain_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM lease_invoice_generations newer JOIN lease_invoice_generations older ON older.invoice_id=newer.predecessor_invoice_id
  JOIN invoices i ON i.id=older.invoice_id JOIN statuses s ON s.id=i.status_id
  WHERE newer.obligation_id<>older.obligation_id OR s.category_key<>'invoice:void') THEN
  RAISE EXCEPTION 'A replaced Lease generation must remain Void in the same billing period';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_generation_chain_guard AFTER INSERT ON lease_invoice_generations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_generation_chain_guard();
CREATE CONSTRAINT TRIGGER lease_superseded_invoice_guard AFTER UPDATE ON invoices DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_generation_chain_guard();
