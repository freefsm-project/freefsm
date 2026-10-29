-- Non-owed periods are schedule dispositions, not fabricated financial
-- obligations/Invoices. Source changes invalidate this mutable projection.
CREATE TABLE lease_billing_period_dispositions (
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 kind text NOT NULL CHECK(kind IN ('initial','monthly')),
 due_on date NOT NULL,
 source_revision bigint NOT NULL,
 reason text NOT NULL,
 PRIMARY KEY(lease_id,kind,due_on)
);

CREATE FUNCTION lease_billing_exemption_review() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target record;
BEGIN
 IF NEW.tax_exempt IS DISTINCT FROM OLD.tax_exempt THEN
  FOR target IN SELECT f.lease_id,f.starts_on FROM lease_finalizations f JOIN leases l ON l.id=f.lease_id WHERE l.customer_id=NEW.id LOOP
   PERFORM record_lease_billing_source_change(target.lease_id,target.starts_on,NULL,'Customer tax exemption changed; review generated billing');
  END LOOP;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER lease_billing_exemption_review AFTER UPDATE OF tax_exempt ON customers FOR EACH ROW EXECUTE FUNCTION lease_billing_exemption_review();
