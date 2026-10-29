-- A move out of a finalized agreement is a change to that agreement too.
-- Retain the immutable generated AND pending snapshots; later revision commands
-- must explicitly reconcile pending obligations rather than using Draft writers.
CREATE OR REPLACE FUNCTION lease_finalized_draft_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='leases' THEN
  IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.id) AND
   (NEW.starts_on,NEW.ends_on,NEW.premises_id,NEW.deleted_at,NEW.lifecycle) IS DISTINCT FROM
   (OLD.starts_on,OLD.ends_on,OLD.premises_id,OLD.deleted_at,OLD.lifecycle) THEN
   RAISE EXCEPTION 'Finalized agreement changes require a reasoned Lease revision';
  END IF;
 ELSE
  IF TG_OP IN ('UPDATE','DELETE') THEN
   IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=OLD.lease_id) THEN
    RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision';
   END IF;
  END IF;
  IF TG_OP IN ('INSERT','UPDATE') THEN
   IF EXISTS(SELECT 1 FROM lease_finalizations WHERE lease_id=NEW.lease_id) THEN
    RAISE EXCEPTION 'Finalized terms and coverage require a reasoned Lease revision';
   END IF;
  END IF;
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
