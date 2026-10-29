CREATE TABLE lease_billing_source_changes (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 starts_on date NOT NULL,
 ends_on date CHECK(ends_on>=starts_on),
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE lease_invoice_review_flags (
 invoice_id bigint NOT NULL REFERENCES lease_invoice_generations(invoice_id),
 change_id bigint NOT NULL REFERENCES lease_billing_source_changes(id),
 PRIMARY KEY(invoice_id,change_id)
);
CREATE TRIGGER lease_source_change_immutable BEFORE UPDATE OR DELETE ON lease_billing_source_changes FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_review_flag_immutable BEFORE UPDATE OR DELETE ON lease_invoice_review_flags FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();

-- Owning writers supply the affected inclusive period-start range. This records
-- review evidence only: it never edits an Invoice or approves replacement terms.
CREATE FUNCTION record_lease_billing_source_change(target bigint,first_day date,last_day date,why text) RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE change bigint;
BEGIN
 PERFORM pg_advisory_xact_lock(190019);
 INSERT INTO lease_billing_source_changes(lease_id,starts_on,ends_on,reason) VALUES(target,first_day,last_day,why) RETURNING id INTO change;
 INSERT INTO lease_invoice_review_flags(invoice_id,change_id)
 SELECT g.invoice_id,change FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id JOIN lease_finalizations f ON f.lease_id=o.lease_id
 WHERE o.lease_id=target AND (CASE WHEN o.kind='initial' THEN f.starts_on ELSE o.due_on END)>=first_day
 AND (last_day IS NULL OR (CASE WHEN o.kind='initial' THEN f.starts_on ELSE o.due_on END)<=last_day);
 RETURN change;
END $$;

CREATE FUNCTION lease_billing_coverage_review() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint; first_day date; last_day date;
BEGIN
 target:=CASE WHEN TG_OP='DELETE' THEN OLD.lease_id ELSE NEW.lease_id END;
 IF NOT EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=target) THEN RETURN NULL; END IF;
 IF TG_OP='UPDATE' AND (OLD.asset_id,OLD.starts_on,OLD.ends_on) IS NOT DISTINCT FROM (NEW.asset_id,NEW.starts_on,NEW.ends_on) THEN RETURN NULL; END IF;
 IF TG_OP='UPDATE' AND OLD.asset_id=NEW.asset_id AND OLD.starts_on=NEW.starts_on THEN
  first_day:=least(coalesce(OLD.ends_on,'infinity'::date),coalesce(NEW.ends_on,'infinity'::date))+1;
  last_day:=CASE WHEN OLD.ends_on IS NULL OR NEW.ends_on IS NULL THEN NULL ELSE greatest(OLD.ends_on,NEW.ends_on) END;
  PERFORM record_lease_billing_source_change(target,first_day,last_day,'Coverage end changed; review this generated period');
 ELSE
  IF TG_OP<>'INSERT' THEN PERFORM record_lease_billing_source_change(target,OLD.starts_on,OLD.ends_on,'Coverage changed; review this generated period'); END IF;
  IF TG_OP<>'DELETE' THEN PERFORM record_lease_billing_source_change(target,NEW.starts_on,NEW.ends_on,'Coverage changed; review this generated period'); END IF;
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER lease_billing_coverage_review AFTER INSERT OR UPDATE OR DELETE ON lease_asset_dependencies FOR EACH ROW EXECUTE FUNCTION lease_billing_coverage_review();

CREATE FUNCTION lease_billing_agreement_review() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=NEW.lease_id) AND
 (NEW.lifecycle,NEW.archived,NEW.premises_id) IS DISTINCT FROM (OLD.lifecycle,OLD.archived,OLD.premises_id) THEN
  PERFORM record_lease_billing_source_change(NEW.lease_id,(SELECT starts_on FROM lease_finalizations WHERE lease_id=NEW.lease_id),NULL,'Agreement dependency changed; review generated billing');
 END IF;
 RETURN NULL;
END $$;
CREATE TRIGGER lease_billing_agreement_review AFTER UPDATE ON lease_master_dependencies FOR EACH ROW EXECUTE FUNCTION lease_billing_agreement_review();
