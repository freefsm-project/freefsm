-- Empty append-only event stores: never infer changes from installed projections.
CREATE TABLE lease_coverage_events (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 actor_id bigint NOT NULL REFERENCES users(id),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE lease_coverage_revisions (
 id bigserial PRIMARY KEY,
 event_id bigint NOT NULL REFERENCES lease_coverage_events(id),
 actor_id bigint NOT NULL REFERENCES users(id),
 operation_key text NOT NULL UNIQUE CHECK(length(operation_key) BETWEEN 16 AND 128),
 intent jsonb NOT NULL,
 action text NOT NULL CHECK(action IN ('create','revise','withdraw','correct')),
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 payload jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE VIEW lease_coverage_current AS
 SELECT DISTINCT ON(e.id) e.id,e.lease_id,r.id AS revision,r.action,r.reason,r.payload,r.recorded_at,r.actor_id
 FROM lease_coverage_events e JOIN lease_coverage_revisions r ON r.event_id=e.id ORDER BY e.id,r.id DESC;
CREATE INDEX lease_coverage_lease ON lease_coverage_events(lease_id,id);
CREATE INDEX lease_coverage_revision_event ON lease_coverage_revisions(event_id,id);
CREATE TRIGGER lease_coverage_event_immutable BEFORE UPDATE OR DELETE ON lease_coverage_events FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_coverage_revision_immutable BEFORE UPDATE OR DELETE ON lease_coverage_revisions FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_coverage_event_lock BEFORE INSERT ON lease_coverage_events FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_coverage_revision_lock BEFORE INSERT ON lease_coverage_revisions FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

ALTER TABLE lease_billing_obligations DROP CONSTRAINT lease_billing_obligations_kind_check;
ALTER TABLE lease_billing_obligations ADD CHECK(kind IN ('initial','monthly','coverage'));
ALTER TABLE lease_billing_period_failures DROP CONSTRAINT lease_billing_period_failures_kind_check;
ALTER TABLE lease_billing_period_failures ADD CHECK(kind IN ('initial','monthly','coverage'));
ALTER TABLE lease_billing_period_dispositions DROP CONSTRAINT lease_billing_period_dispositions_kind_check;
ALTER TABLE lease_billing_period_dispositions ADD CHECK(kind IN ('initial','monthly','coverage'));

-- Native event-managed coverage must remain complete even when a later writer
-- uses the shared dependency seam. Retained legacy projections are not remapped.
CREATE FUNCTION lease_event_coverage_validate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint; commencement date; last_day date; state text; spans datemultirange;
BEGIN
 IF TG_OP='DELETE' THEN target:=OLD.lease_id; ELSE target:=NEW.lease_id; END IF;
 IF NOT EXISTS(SELECT 1 FROM lease_coverage_events WHERE lease_id=target) THEN RETURN NULL; END IF;
 SELECT l.starts_on,d.lifecycle INTO commencement,state FROM leases l JOIN lease_master_dependencies d ON d.lease_id=l.id WHERE l.id=target;
 IF state='draft' THEN RAISE EXCEPTION 'Finalized coverage cannot become Draft'; END IF;
 IF state='terminated' THEN
  IF EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=target AND ends_on IS NULL) THEN RAISE EXCEPTION 'Terminated coverage requires its inclusive end'; END IF;
  SELECT max(ends_on) INTO last_day FROM lease_asset_dependencies WHERE lease_id=target;
 END IF;
 SELECT range_agg(daterange(starts_on,ends_on,'[]')) INTO spans FROM lease_asset_dependencies WHERE lease_id=target;
 IF spans IS NULL OR NOT spans @> daterange(commencement,last_day,'[]') OR EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=target AND starts_on<commencement) THEN
  RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='At least one Asset must be covered throughout the in-force Lease timeline';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER lease_event_coverage_complete AFTER INSERT OR UPDATE OR DELETE ON lease_asset_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_event_coverage_validate();
CREATE CONSTRAINT TRIGGER lease_event_master_complete AFTER INSERT OR UPDATE ON lease_master_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION lease_event_coverage_validate();
