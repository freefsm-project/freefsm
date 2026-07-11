-- Maintenance-only cutover. Every validation raises before destructive changes.
DO $$
DECLARE totals_count bigint; payments_count bigint;
BEGIN
  IF to_regclass('settlement_migration_invoice_totals') IS NULL THEN
    RAISE EXCEPTION 'settlement preflight: canonical invoice totals missing; run through application migrator';
  END IF;
  EXECUTE 'SELECT count(*) FROM settlement_migration_invoice_totals' INTO totals_count;
  IF totals_count <> (SELECT count(*) FROM invoices) THEN
    RAISE EXCEPTION 'settlement preflight: canonical invoice totals incomplete; run through application migrator';
  END IF;
  IF to_regclass('settlement_migration_payments') IS NULL THEN
    RAISE EXCEPTION 'settlement preflight: canonical legacy payments missing; run through application migrator';
  END IF;
  EXECUTE 'SELECT count(*) FROM settlement_migration_payments' INTO payments_count;
  IF payments_count <> (SELECT coalesce(sum(jsonb_array_length(payments)),0) FROM invoices) THEN
    RAISE EXCEPTION 'settlement preflight: canonical legacy payments incomplete; run through application migrator';
  END IF;
  IF EXISTS (SELECT 1 FROM invoices WHERE company_id IS NULL) THEN
    RAISE EXCEPTION 'settlement preflight: missing company ownership: invoices=%', (SELECT count(*) FROM invoices WHERE company_id IS NULL);
  END IF;
  IF EXISTS (SELECT 1 FROM customers WHERE company_id IS NULL) THEN
    RAISE EXCEPTION 'settlement preflight: missing company ownership: customers=%', (SELECT count(*) FROM customers WHERE company_id IS NULL);
  END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i WHERE jsonb_array_length(i.payments)>0 AND NOT EXISTS(
      SELECT 1 FROM users u WHERE u.company_id=i.company_id AND u.role IN ('admin','dispatcher'))
  ) THEN RAISE EXCEPTION 'settlement preflight: legacy payment has no authorized migration actor'; END IF;
  IF EXISTS (SELECT 1 FROM invoices WHERE customer_id IS NULL) THEN
    RAISE EXCEPTION 'settlement preflight: orphan invoice(s) have no customer';
  END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i JOIN customers c ON c.id = i.customer_id
    WHERE i.company_id IS DISTINCT FROM c.company_id
  ) THEN RAISE EXCEPTION 'settlement preflight: invoice/customer company mismatch'; END IF;
  IF EXISTS (SELECT 1 FROM invoices WHERE jsonb_typeof(payments) IS DISTINCT FROM 'array') THEN
    RAISE EXCEPTION 'settlement preflight: payments must be JSON arrays';
  END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i CROSS JOIN LATERAL jsonb_array_elements(i.payments) p
    WHERE jsonb_typeof(p) <> 'object'
       OR NOT (p ? 'amount' AND p ? 'method' AND p ? 'date')
       OR jsonb_typeof(p->'amount') <> 'number'
       OR (p->>'amount')::numeric <= 0
       OR p->>'method' NOT IN ('cash','check','credit_card','transfer','other')
       OR p->>'date' !~ '^\d{4}-\d{2}-\d{2}$'
  ) THEN RAISE EXCEPTION 'settlement preflight: malformed or invalid payment JSON'; END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i CROSS JOIN LATERAL jsonb_array_elements(i.payments) p
     WHERE (p->>'date')::date > (CURRENT_TIMESTAMP AT TIME ZONE
      (SELECT cs.timezone FROM company_settings cs WHERE cs.company_id=i.company_id LIMIT 1))::date
  ) THEN RAISE EXCEPTION 'settlement preflight: future payment date'; END IF;
  IF EXISTS (SELECT 1 FROM invoices i WHERE NOT EXISTS (
    SELECT 1 FROM company_settings cs JOIN pg_timezone_names z ON z.name=cs.timezone WHERE cs.company_id=i.company_id
  )) THEN RAISE EXCEPTION 'settlement preflight: missing or invalid company timezone'; END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i CROSS JOIN LATERAL jsonb_array_elements(i.payments) p
    WHERE nullif(trim(p->>'id'),'') IS NOT NULL
    GROUP BY i.company_id,p->>'id' HAVING count(*) > 1
  ) THEN RAISE EXCEPTION 'settlement preflight: duplicate legacy payment id'; END IF;
  IF EXISTS (
    SELECT 1 FROM invoices i JOIN statuses s ON s.id=i.status_id
    WHERE lower(s.name)='void' AND jsonb_array_length(i.payments)>0
  ) THEN RAISE EXCEPTION 'settlement preflight: void invoice has payments'; END IF;
END $$;

ALTER TABLE invoices ADD COLUMN settlement_state TEXT NOT NULL DEFAULT 'unpaid'
  CHECK (settlement_state IN ('unpaid','partially_paid','paid'));
ALTER TABLE customers ADD CONSTRAINT customers_id_company_unique UNIQUE(id,company_id);
ALTER TABLE users ADD CONSTRAINT users_id_company_unique UNIQUE(id,company_id);
ALTER TABLE invoices ADD CONSTRAINT invoices_id_customer_company_unique UNIQUE(id,customer_id,company_id);

CREATE TABLE settlement_idempotency (
  company_id BIGINT NOT NULL REFERENCES companies(id),
  operation TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  request_fingerprint TEXT NOT NULL,
  result_id UUID,
  result_json JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY(company_id, operation, idempotency_key)
);

CREATE TABLE invoice_payments (
  id UUID PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id),
  customer_id BIGINT NOT NULL,
  invoice_id BIGINT NOT NULL,
  amount_cents BIGINT NOT NULL CHECK(amount_cents > 0),
  method TEXT NOT NULL CHECK(method IN ('cash','check','credit_card','transfer','other')),
  received_date DATE NOT NULL,
  reference TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '',
  actor_id BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY(customer_id,company_id) REFERENCES customers(id,company_id),
  FOREIGN KEY(invoice_id,customer_id,company_id) REFERENCES invoices(id,customer_id,company_id),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id)
);
CREATE INDEX invoice_payments_invoice_idx ON invoice_payments(invoice_id, received_date, id);
ALTER TABLE invoice_payments ADD CONSTRAINT invoice_payments_id_invoice_unique UNIQUE(id,invoice_id);
ALTER TABLE invoice_payments ADD CONSTRAINT invoice_payments_id_customer_company_unique UNIQUE(id,customer_id,company_id);

CREATE TABLE payment_invoice_allocations (
  payment_id UUID PRIMARY KEY, invoice_id BIGINT NOT NULL, amount_cents BIGINT NOT NULL CHECK(amount_cents > 0),
  FOREIGN KEY(payment_id,invoice_id) REFERENCES invoice_payments(id,invoice_id)
);

CREATE TABLE customer_credits (
  id UUID PRIMARY KEY, company_id BIGINT NOT NULL REFERENCES companies(id),
  customer_id BIGINT NOT NULL,
  source_payment_id UUID NOT NULL UNIQUE,
  original_amount_cents BIGINT NOT NULL CHECK(original_amount_cents > 0),
  source_date DATE NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY(customer_id,company_id) REFERENCES customers(id,company_id),
  FOREIGN KEY(source_payment_id,customer_id,company_id) REFERENCES invoice_payments(id,customer_id,company_id)
);
CREATE INDEX customer_credits_fifo_idx ON customer_credits(customer_id, source_date, id);
ALTER TABLE customer_credits ADD CONSTRAINT customer_credits_id_customer_company_unique UNIQUE(id,customer_id,company_id);

CREATE TABLE credit_applications (
  id UUID PRIMARY KEY, company_id BIGINT NOT NULL REFERENCES companies(id),
  customer_id BIGINT NOT NULL, invoice_id BIGINT NOT NULL,
  credit_id UUID NOT NULL, amount_cents BIGINT NOT NULL CHECK(amount_cents > 0),
  effective_date DATE NOT NULL, actor_id BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY(customer_id,company_id) REFERENCES customers(id,company_id),
  FOREIGN KEY(invoice_id,customer_id,company_id) REFERENCES invoices(id,customer_id,company_id),
  FOREIGN KEY(credit_id,customer_id,company_id) REFERENCES customer_credits(id,customer_id,company_id),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id)
);

CREATE TABLE credit_refunds (
  id UUID PRIMARY KEY, company_id BIGINT NOT NULL REFERENCES companies(id), customer_id BIGINT NOT NULL,
  amount_cents BIGINT NOT NULL CHECK(amount_cents > 0), method TEXT NOT NULL CHECK(method IN ('cash','check','credit_card','transfer','other')),
  effective_date DATE NOT NULL, reference TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL CHECK(length(trim(reason))>0),
  actor_id BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY(customer_id,company_id) REFERENCES customers(id,company_id),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id)
);
CREATE TABLE credit_refund_allocations (
  refund_id UUID NOT NULL REFERENCES credit_refunds(id), credit_id UUID NOT NULL REFERENCES customer_credits(id),
  amount_cents BIGINT NOT NULL CHECK(amount_cents > 0), PRIMARY KEY(refund_id, credit_id)
);

CREATE TABLE settlement_reversals (
  id UUID PRIMARY KEY, company_id BIGINT NOT NULL REFERENCES companies(id),
  operation_type TEXT NOT NULL CHECK(operation_type IN ('payment','credit_application','credit_refund')),
  operation_id UUID NOT NULL, reason TEXT NOT NULL CHECK(length(trim(reason)) > 0),
  actor_id BIGINT NOT NULL, effective_date DATE NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id),
  UNIQUE(operation_type, operation_id)
);

-- Deterministic legacy conversion in invoice/date/array order. Canonical totals must
-- be proven by the application preflight before this migration is run.
WITH legacy AS (
  SELECT i.id invoice_id,i.company_id,i.customer_id,p.ordinal ordinal,p.received_date,p.amount_cents,p.method,p.reference,p.notes
  FROM invoices i JOIN settlement_migration_payments p ON p.invoice_id=i.id
), inserted AS (
  INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,reference,notes,actor_id)
  SELECT md5(l.invoice_id::text||':'||l.ordinal::text)::uuid,l.company_id,l.customer_id,l.invoice_id,l.amount_cents,
         l.method,l.received_date,l.reference,l.notes,
         (SELECT u.id FROM users u WHERE u.company_id=l.company_id AND u.role IN ('admin','dispatcher') ORDER BY (u.role='admin') DESC,u.id LIMIT 1)
  FROM legacy l RETURNING *
)
SELECT count(*) FROM inserted;

WITH ordered AS (
  SELECT p.*, t.total_cents, legacy.ordinal,
    coalesce(sum(p.amount_cents) OVER (PARTITION BY p.invoice_id ORDER BY p.received_date,legacy.ordinal ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING),0) prior
  FROM invoice_payments p JOIN settlement_migration_invoice_totals t ON t.invoice_id=p.invoice_id
  JOIN settlement_migration_payments legacy ON legacy.invoice_id=p.invoice_id AND p.id=md5(p.invoice_id::text||':'||legacy.ordinal::text)::uuid
), allocations AS (
  INSERT INTO payment_invoice_allocations(payment_id,invoice_id,amount_cents)
  SELECT id,invoice_id,least(amount_cents,greatest(total_cents-prior,0)) FROM ordered
  WHERE least(amount_cents,greatest(total_cents-prior,0)) > 0 RETURNING payment_id
)
INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date)
SELECT md5('credit:'||o.id::text)::uuid,o.company_id,o.customer_id,o.id,
       o.amount_cents-least(o.amount_cents,greatest(o.total_cents-o.prior,0)),o.received_date
FROM ordered o WHERE o.amount_cents-least(o.amount_cents,greatest(o.total_cents-o.prior,0)) > 0;

UPDATE invoices i SET settlement_state = CASE
  WHEN t.total_cents=0 OR coalesce(a.paid,0)>=t.total_cents THEN 'paid'
  WHEN coalesce(a.paid,0)>0 THEN 'partially_paid' ELSE 'unpaid' END
FROM settlement_migration_invoice_totals t
LEFT JOIN (SELECT invoice_id,sum(amount_cents) paid FROM payment_invoice_allocations GROUP BY invoice_id) a ON a.invoice_id=t.invoice_id
WHERE i.id=t.invoice_id;

UPDATE invoices i SET status_id = sent.id
FROM statuses old, LATERAL (
  SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id
  WHERE w.object_type='invoice' AND lower(s.name)='sent' AND s.company_id IS NOT DISTINCT FROM old.company_id LIMIT 1
) sent
WHERE i.status_id=old.id AND lower(old.name) IN ('paid','partially paid');
DELETE FROM statuses s USING status_workflows w WHERE s.workflow_id=w.id AND w.object_type='invoice' AND lower(s.name) IN ('paid','partially paid');

ALTER TABLE invoices ALTER COLUMN customer_id SET NOT NULL;
ALTER TABLE invoices DROP COLUMN payments;
DROP TABLE settlement_migration_invoice_totals;
DROP TABLE settlement_migration_payments;

-- Originals are immutable at the database boundary.
CREATE FUNCTION reject_settlement_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'financial originals are immutable'; END $$;
CREATE TRIGGER invoice_payments_immutable BEFORE UPDATE OR DELETE ON invoice_payments FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER payment_allocations_immutable BEFORE UPDATE OR DELETE ON payment_invoice_allocations FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER customer_credits_immutable BEFORE UPDATE OR DELETE ON customer_credits FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER credit_applications_immutable BEFORE UPDATE OR DELETE ON credit_applications FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER credit_refunds_immutable BEFORE UPDATE OR DELETE ON credit_refunds FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER refund_allocations_immutable BEFORE UPDATE OR DELETE ON credit_refund_allocations FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();
CREATE TRIGGER settlement_reversals_immutable BEFORE UPDATE OR DELETE ON settlement_reversals FOR EACH ROW EXECUTE FUNCTION reject_settlement_mutation();

CREATE FUNCTION validate_settlement_reversal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.operation_type='payment' AND NOT EXISTS(SELECT 1 FROM invoice_payments WHERE id=NEW.operation_id AND company_id=NEW.company_id) THEN RAISE EXCEPTION 'reversal/payment company mismatch'; END IF;
  IF NEW.operation_type='credit_application' AND NOT EXISTS(SELECT 1 FROM credit_applications WHERE id=NEW.operation_id AND company_id=NEW.company_id) THEN RAISE EXCEPTION 'reversal/application company mismatch'; END IF;
  IF NEW.operation_type='credit_refund' AND NOT EXISTS(SELECT 1 FROM credit_refunds WHERE id=NEW.operation_id AND company_id=NEW.company_id) THEN RAISE EXCEPTION 'reversal/refund company mismatch'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER settlement_reversal_ownership BEFORE INSERT ON settlement_reversals FOR EACH ROW EXECUTE FUNCTION validate_settlement_reversal();

CREATE FUNCTION validate_refund_allocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS(SELECT 1 FROM credit_refunds f JOIN customer_credits c ON c.id=NEW.credit_id WHERE f.id=NEW.refund_id AND f.company_id=c.company_id AND f.customer_id=c.customer_id) THEN
    RAISE EXCEPTION 'refund allocation source ownership mismatch';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER refund_allocation_ownership BEFORE INSERT ON credit_refund_allocations FOR EACH ROW EXECUTE FUNCTION validate_refund_allocation();

CREATE FUNCTION invoice_has_active_settlement(invoice BIGINT) RETURNS boolean LANGUAGE sql STABLE AS $$
  SELECT EXISTS(SELECT 1 FROM invoice_payments p WHERE p.invoice_id=invoice AND NOT EXISTS(
    SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id))
  OR EXISTS(SELECT 1 FROM credit_applications a WHERE a.invoice_id=invoice AND NOT EXISTS(
    SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id));
$$;
CREATE FUNCTION guard_settled_invoice_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE status_name text;
BEGIN
  IF invoice_has_active_settlement(OLD.id) AND
     (NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate) THEN
    RAISE EXCEPTION 'settled invoice monetary fields and customer are locked';
  END IF;
  IF NEW.status_id IS DISTINCT FROM OLD.status_id THEN
    SELECT lower(name) INTO status_name FROM statuses WHERE id=NEW.status_id;
    IF status_name='void' AND invoice_has_active_settlement(OLD.id) THEN RAISE EXCEPTION 'void requires zero active settlement'; END IF;
  END IF;
  IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
    SELECT lower(name) INTO status_name FROM statuses WHERE id=NEW.status_id;
    IF coalesce(status_name,'')<>'void' AND NEW.settlement_state<>'paid' THEN
      RAISE EXCEPTION 'invoice archive requires paid or void';
    END IF;
  END IF;
  IF NEW.settlement_state IS DISTINCT FROM OLD.settlement_state AND NOT (
       NOT invoice_has_active_settlement(OLD.id) AND
       (NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate)
     ) AND
     current_setting('freefsm.settlement_projection',true) IS DISTINCT FROM 'on' THEN
    RAISE EXCEPTION 'settlement_state is ledger-derived';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER settled_invoice_guard BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION guard_settled_invoice_update();

CREATE FUNCTION settlement_set_invoice_state(invoice BIGINT, state TEXT) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  IF state NOT IN ('unpaid','partially_paid','paid') THEN RAISE EXCEPTION 'invalid settlement state'; END IF;
  PERFORM set_config('freefsm.settlement_projection','on',true);
  UPDATE invoices SET settlement_state=state,updated_at=NOW() WHERE id=invoice;
  PERFORM set_config('freefsm.settlement_projection','off',true);
END $$;

CREATE FUNCTION guard_customer_archive() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL AND EXISTS(
    SELECT 1 FROM customer_credits c WHERE c.customer_id=OLD.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals sr WHERE sr.operation_type='payment' AND sr.operation_id=c.source_payment_id) AND c.original_amount_cents >
      coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0) +
      coalesce((SELECT sum(ra.amount_cents) FROM credit_refund_allocations ra WHERE ra.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=ra.refund_id)),0)
  ) THEN RAISE EXCEPTION 'customer has unresolved credit'; END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER customer_credit_archive_guard BEFORE UPDATE ON customers FOR EACH ROW EXECUTE FUNCTION guard_customer_archive();
