-- Corrections retain original commencement and all commercial provenance.
CREATE TABLE lease_fact_corrections (
 id bigserial PRIMARY KEY,
 lease_id bigint NOT NULL REFERENCES lease_finalizations(lease_id),
 actor_id bigint NOT NULL REFERENCES users(id),
 operation_key text NOT NULL UNIQUE CHECK(length(operation_key) BETWEEN 16 AND 128),
 intent jsonb NOT NULL,
 reason text NOT NULL CHECK(length(btrim(reason))>0),
 before_value jsonb NOT NULL,
 after_value jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX lease_fact_correction_history ON lease_fact_corrections(lease_id,id DESC);
CREATE TRIGGER lease_fact_correction_immutable BEFORE UPDATE OR DELETE ON lease_fact_corrections FOR EACH ROW EXECUTE FUNCTION lease_finalization_immutable();
CREATE TRIGGER lease_fact_correction_lock BEFORE INSERT ON lease_fact_corrections FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();

-- A projection update must be backed by a new immutable correction in this
-- transaction. Neither a session flag nor an old matching revision is authority.
CREATE OR REPLACE FUNCTION lease_finalized_draft_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='leases' THEN
  IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.id) THEN
   IF NEW.lifecycle IS DISTINCT FROM OLD.lifecycle OR
    (NEW.deleted_at IS DISTINCT FROM OLD.deleted_at AND NOT lease_has_ended(OLD.id)) THEN
    RAISE EXCEPTION 'Finalized agreement changes require a reasoned Lease revision';
   END IF;
   IF (NEW.starts_on,NEW.ends_on,NEW.premises_id) IS DISTINCT FROM (OLD.starts_on,OLD.ends_on,OLD.premises_id)
    AND NOT EXISTS(SELECT 1 FROM lease_fact_corrections c WHERE c.lease_id=OLD.id
     AND c.xmin::text=pg_current_xact_id()::text
     AND c.before_value->>'starts_on'=OLD.starts_on::text
     AND c.before_value->>'ends_on'=OLD.ends_on::text
     AND (c.before_value->>'premises_id')::bigint IS NOT DISTINCT FROM OLD.premises_id
     AND c.after_value->>'starts_on'=NEW.starts_on::text
     AND c.after_value->>'ends_on'=NEW.ends_on::text
     AND (c.after_value->>'premises_id')::bigint IS NOT DISTINCT FROM NEW.premises_id) THEN
    RAISE EXCEPTION 'Finalized agreement changes require a reasoned Lease revision';
   END IF;
  END IF;
 ELSE
  IF TG_OP IN ('UPDATE','DELETE') AND EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.lease_id) THEN RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision'; END IF;
  IF TG_OP IN ('INSERT','UPDATE') AND EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=NEW.lease_id) THEN RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision'; END IF;
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
