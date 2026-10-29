-- Historical consistency is independent of present-day archive eligibility.
-- Draft selections reserve nothing; every retained finalized interval is real
-- coverage, including Terminated and archived history. Do not change any dates.
-- Fence the preflight and replacement against already-running history writers.
SELECT pg_advisory_xact_lock(190019);

CREATE OR REPLACE FUNCTION lease_asset_timeline_validate(asset bigint) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS (
  SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id)
  WHERE d.asset_id=asset AND l.lifecycle='terminated' AND d.ends_on IS NULL
 ) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Terminated Lease coverage with unknown end requires explicit mapping review';
 END IF;
 -- An unknown-start observation cannot prove a conflicting placement's start.
 -- Its known return bounds it: a return at/before commencement is disjoint.
 -- Otherwise request factual review rather than manufacture an infinite period.
 IF EXISTS (
  SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id)
  JOIN asset_association_periods p ON p.asset_id=d.asset_id
  WHERE d.asset_id=asset AND l.lifecycle<>'draft'
   AND p.customer_id IS NOT NULL AND p.customer_id<>l.customer_id
   AND p.starts_on IS NULL AND (p.ends_before IS NULL OR p.ends_before>d.starts_on)
 ) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Unknown-start Customer placement against retained Lease coverage requires explicit mapping review';
 END IF;
 IF EXISTS (
  SELECT 1 FROM lease_asset_dependencies d JOIN lease_master_dependencies l USING(lease_id)
  JOIN asset_association_periods p ON p.asset_id=d.asset_id
  WHERE d.asset_id=asset AND l.lifecycle<>'draft'
   AND p.customer_id IS NOT NULL AND p.customer_id<>l.customer_id
   AND p.starts_on IS NOT NULL
   AND daterange(p.starts_on,p.ends_before,'[)') && daterange(d.starts_on,d.ends_on,'[]')
 ) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage conflicts with a recorded Customer placement period';
 END IF;
END $$;

-- Existing incompatible facts must be reviewed through the owning history
-- commands before retrying this migration, not silently rewritten or ignored.
DO $$ DECLARE asset bigint;
BEGIN
 FOR asset IN SELECT DISTINCT d.asset_id FROM lease_asset_dependencies d
  JOIN lease_master_dependencies l USING(lease_id) WHERE l.lifecycle<>'draft'
 LOOP
  BEGIN
   PERFORM lease_asset_timeline_validate(asset);
  EXCEPTION WHEN check_violation THEN
   RAISE EXCEPTION 'Retained Lease placement requires explicit mapping review (Asset %): %', asset, SQLERRM;
  END;
 END LOOP;
END $$;
