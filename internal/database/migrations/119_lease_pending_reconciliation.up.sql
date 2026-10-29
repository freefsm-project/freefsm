CREATE TABLE lease_billing_obligation_revisions (
 id bigserial PRIMARY KEY,
 obligation_id bigint NOT NULL REFERENCES lease_billing_obligations(id),
 financial text NOT NULL,
 line_items jsonb NOT NULL,
 source jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX lease_obligation_revision_latest ON lease_billing_obligation_revisions(obligation_id,id DESC);
CREATE TRIGGER lease_obligation_revision_immutable BEFORE UPDATE OR DELETE ON lease_billing_obligation_revisions FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE VIEW lease_billing_obligation_current AS
 SELECT o.id,o.lease_id,o.kind,o.due_on,coalesce(r.financial,o.financial) financial,coalesce(r.line_items,o.line_items) line_items,r.id revision_id
 FROM lease_billing_obligations o LEFT JOIN LATERAL (SELECT * FROM lease_billing_obligation_revisions WHERE obligation_id=o.id ORDER BY id DESC LIMIT 1) r ON true;
CREATE FUNCTION lease_pending_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM pg_advisory_xact_lock(190019);
 IF EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=NEW.obligation_id) THEN
  RAISE EXCEPTION 'Generated Lease obligations cannot be revised';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_pending_revision_guard BEFORE INSERT ON lease_billing_obligation_revisions FOR EACH ROW EXECUTE FUNCTION lease_pending_revision_guard();
ALTER TABLE lease_invoice_generations ADD COLUMN obligation_revision_id bigint REFERENCES lease_billing_obligation_revisions(id);
CREATE FUNCTION lease_generation_revision_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE source lease_billing_obligation_current%ROWTYPE;
BEGIN
 SELECT * INTO STRICT source FROM lease_billing_obligation_current WHERE id=NEW.obligation_id;
 NEW.obligation_revision_id:=source.revision_id;
 IF NOT EXISTS(SELECT 1 FROM invoices WHERE id=NEW.invoice_id AND financial=source.financial AND line_items=source.line_items) THEN
  RAISE EXCEPTION 'Generation must capture the approved obligation revision';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_generation_revision_guard BEFORE INSERT ON lease_invoice_generations FOR EACH ROW EXECUTE FUNCTION lease_generation_revision_guard();
CREATE TABLE lease_billing_period_failures (
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 kind text NOT NULL CHECK(kind IN ('initial','monthly')),
 due_on date NOT NULL,
 attempted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 failure text NOT NULL,
 PRIMARY KEY(lease_id,kind,due_on)
);
