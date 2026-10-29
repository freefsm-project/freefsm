-- No legacy agreement, charge, or permission is inferred by this migration.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM lease_asset_dependencies a JOIN lease_master_dependencies la ON la.lease_id=a.lease_id
  JOIN lease_asset_dependencies b ON b.asset_id=a.asset_id AND b.lease_id>a.lease_id
  JOIN lease_master_dependencies lb ON lb.lease_id=b.lease_id
  WHERE la.lifecycle<>'draft' AND lb.lifecycle<>'draft'
   AND daterange(a.starts_on,a.ends_on,'[]') && daterange(b.starts_on,b.ends_on,'[]')) THEN
  RAISE EXCEPTION 'Existing overlapping Lease coverage requires explicit mapping review; recorded facts are retained';
 END IF;
END $$;
ALTER TABLE leases DROP CONSTRAINT leases_lifecycle_check;
ALTER TABLE leases ADD CONSTRAINT leases_lifecycle_check CHECK(lifecycle IN ('draft','scheduled','active','month_to_month','terminated'));
CREATE TABLE lease_finalizations (
 lease_id bigint PRIMARY KEY REFERENCES leases(id),
 actor_id bigint NOT NULL REFERENCES users(id),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 details_revision bigint NOT NULL,
 billing_revision bigint NOT NULL,
 managed_billing_start date NOT NULL,
 starts_on date NOT NULL,
 fixed_ends_on date NOT NULL CHECK(fixed_ends_on>=starts_on),
 initial_due_on date NOT NULL CHECK(initial_due_on<=starts_on),
 premises_snapshot jsonb NOT NULL,
 terms jsonb NOT NULL,
 monthly_anchor date NOT NULL
);
CREATE TABLE lease_billing_obligations (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 kind text NOT NULL CHECK(kind IN ('initial','monthly')),
 due_on date NOT NULL,
 financial text NOT NULL,
 line_items jsonb NOT NULL,
 UNIQUE(lease_id,kind,due_on)
);
CREATE UNIQUE INDEX lease_one_initial_obligation ON lease_billing_obligations(lease_id) WHERE kind='initial';
CREATE TABLE lease_invoice_generations (
 obligation_id bigint PRIMARY KEY REFERENCES lease_billing_obligations(id),
 invoice_id bigint NOT NULL UNIQUE REFERENCES invoices(id),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE invoices ADD COLUMN lease_id bigint REFERENCES leases(id);
CREATE FUNCTION lease_finalization_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Lease commencement, obligations and generation history are immutable'; END $$;
CREATE TRIGGER lease_finalization_immutable BEFORE UPDATE OR DELETE ON lease_finalizations FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_obligation_immutable BEFORE UPDATE OR DELETE ON lease_billing_obligations FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_generation_immutable BEFORE UPDATE OR DELETE ON lease_invoice_generations FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE FUNCTION lease_finalized_draft_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='leases' THEN
  IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.id) AND
   (NEW.starts_on,NEW.ends_on,NEW.premises_id,NEW.deleted_at,NEW.lifecycle) IS DISTINCT FROM
   (OLD.starts_on,OLD.ends_on,OLD.premises_id,OLD.deleted_at,OLD.lifecycle) THEN
   RAISE EXCEPTION 'Finalized agreement changes require a reasoned Lease revision';
  END IF;
 ELSE
  IF TG_OP='DELETE' THEN target:=OLD.lease_id; ELSE target:=NEW.lease_id; END IF;
  IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=target) THEN
   RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision';
  END IF;
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER lease_finalized_draft_guard BEFORE UPDATE ON leases FOR EACH ROW EXECUTE FUNCTION lease_finalized_draft_guard();
CREATE TRIGGER lease_finalized_terms_guard BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_asset_terms FOR EACH ROW EXECUTE FUNCTION lease_finalized_draft_guard();
CREATE TRIGGER lease_finalized_billing_guard BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_billing FOR EACH ROW EXECUTE FUNCTION lease_finalized_draft_guard();
CREATE TRIGGER lease_finalized_assets_guard BEFORE INSERT OR UPDATE OR DELETE ON lease_intended_assets FOR EACH ROW EXECUTE FUNCTION lease_finalized_draft_guard();
CREATE FUNCTION lease_invoice_source_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.lease_id IS NOT NULL AND (NEW.lease_id,NEW.customer_id) IS DISTINCT FROM (OLD.lease_id,OLD.customer_id) THEN
  RAISE EXCEPTION 'Lease Invoice source and Customer are immutable';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_invoice_source_guard BEFORE UPDATE ON invoices FOR EACH ROW EXECUTE FUNCTION lease_invoice_source_guard();
CREATE FUNCTION lease_generation_consistency() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='invoices' THEN target:=NEW.id; ELSE target:=NEW.invoice_id; END IF;
 IF EXISTS(SELECT 1 FROM invoices i WHERE i.id=target AND i.lease_id IS NOT NULL AND NOT EXISTS(
  SELECT 1 FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id
  JOIN leases l ON l.id=o.lease_id WHERE g.invoice_id=i.id AND o.lease_id=i.lease_id AND l.customer_id=i.customer_id AND l.company_id=i.company_id))
 OR EXISTS(SELECT 1 FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id
  JOIN invoices i ON i.id=g.invoice_id WHERE i.id=target AND i.lease_id IS DISTINCT FROM o.lease_id) THEN
  RAISE EXCEPTION 'Lease Invoice and durable obligation provenance must commit together';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_invoice_generation_consistency AFTER INSERT OR UPDATE ON invoices DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_generation_consistency();
CREATE CONSTRAINT TRIGGER lease_generation_invoice_consistency AFTER INSERT ON lease_invoice_generations DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_generation_consistency();

-- The dependency seam formerly guarded placement only. Coverage is inclusive;
-- retained terminated/archived intervals still participate in history checks.
CREATE FUNCTION lease_coverage_nonoverlap() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 target:=NEW.lease_id;
 IF EXISTS(SELECT 1 FROM lease_asset_dependencies a JOIN lease_master_dependencies la ON la.lease_id=a.lease_id
  JOIN lease_asset_dependencies b ON b.asset_id=a.asset_id AND b.lease_id<>a.lease_id
  JOIN lease_master_dependencies lb ON lb.lease_id=b.lease_id
  WHERE a.lease_id=target AND la.lifecycle<>'draft' AND lb.lifecycle<>'draft'
   AND daterange(a.starts_on,a.ends_on,'[]') && daterange(b.starts_on,b.ends_on,'[]')) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='An Asset cannot have overlapping finalized Lease coverage';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_coverage_nonoverlap AFTER INSERT OR UPDATE ON lease_asset_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_coverage_nonoverlap();
CREATE CONSTRAINT TRIGGER lease_coverage_lifecycle_nonoverlap AFTER INSERT OR UPDATE ON lease_master_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_coverage_nonoverlap();
