-- Review is supplied by the migration runner, never inferred from Customer links.
ALTER TABLE assets ADD COLUMN IF NOT EXISTS number text;
ALTER TABLE assets ADD COLUMN IF NOT EXISTS ownership_class text;
CREATE TABLE asset_identity_migration_evidence (
 snapshot text PRIMARY KEY, evidence jsonb NOT NULL, resolutions jsonb NOT NULL,
 reviewer text NOT NULL, reason text NOT NULL, reviewed_at timestamptz NOT NULL DEFAULT now()
);
DO $$
DECLARE review jsonb := nullif(current_setting('freefsm.asset_review',true),'')::jsonb;
BEGIN
 IF EXISTS(SELECT 1 FROM assets) THEN
  IF review IS NULL THEN RAISE EXCEPTION 'Asset identity review required before cutover'; END IF;
  INSERT INTO asset_identity_migration_evidence(snapshot,evidence,resolutions,reviewer,reason)
   VALUES(review->>'snapshot',review->'evidence',review->'resolutions',review->>'reviewer',review->>'reason');
  UPDATE assets a SET number=r->>'number',ownership_class=r->>'ownership_class'
   FROM jsonb_array_elements(review->'resolutions') r WHERE a.id=(r->>'id')::bigint;
 END IF;
END $$;
ALTER TABLE assets ALTER COLUMN number SET NOT NULL;
ALTER TABLE assets ALTER COLUMN ownership_class SET NOT NULL;
ALTER TABLE assets ALTER COLUMN customer_id DROP NOT NULL;
ALTER TABLE assets ADD CONSTRAINT asset_number_nonblank CHECK(btrim(number)<>'' AND number !~ '[[:cntrl:]]');
CREATE UNIQUE INDEX asset_number_identity ON assets(number);
ALTER TABLE assets ADD CONSTRAINT asset_ownership_class CHECK(ownership_class IN ('customer_owned','company_owned'));
ALTER TABLE assets ADD CONSTRAINT asset_owner_required CHECK(ownership_class<>'customer_owned' OR customer_id IS NOT NULL);
ALTER TABLE assets ADD CONSTRAINT asset_unplaced_location CHECK(customer_id IS NOT NULL OR location_id IS NULL);
ALTER TABLE assets DROP CONSTRAINT assets_customer_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_customer_id_fkey FOREIGN KEY(customer_id) REFERENCES customers(id) ON DELETE RESTRICT;
ALTER TABLE assets DROP CONSTRAINT assets_location_id_fkey;
ALTER TABLE assets ADD CONSTRAINT assets_location_id_fkey FOREIGN KEY(location_id) REFERENCES locations(id) ON DELETE RESTRICT;
CREATE SEQUENCE asset_number_seq;
CREATE FUNCTION next_asset_number() RETURNS text LANGUAGE plpgsql AS $$
DECLARE candidate text;
BEGIN
 LOOP
  candidate:='AST-'||nextval('asset_number_seq')::text;
  IF NOT EXISTS(SELECT 1 FROM assets WHERE number=candidate) THEN RETURN candidate; END IF;
 END LOOP;
END $$;
ALTER TABLE assets ALTER COLUMN number SET DEFAULT next_asset_number();

CREATE FUNCTION asset_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Asset identity is retained; archive instead'; END IF;
 IF TG_OP='UPDATE' THEN
  IF NEW.number IS DISTINCT FROM OLD.number OR NEW.ownership_class IS DISTINCT FROM OLD.ownership_class THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Asset Number and ownership class are immutable';
  END IF;
  IF NEW.customer_id IS DISTINCT FROM OLD.customer_id THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Use an explicit Asset association action';
  END IF;
 END IF;
 -- Lock selected masters against archive through commit. Retained unavailable
 -- selections do not prevent unrelated descriptive edits.
 IF TG_OP='INSERT' OR NEW.location_id IS DISTINCT FROM OLD.location_id OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL) THEN
  IF NEW.customer_id IS NOT NULL THEN
   PERFORM 1 FROM customers WHERE id=NEW.customer_id AND (NEW.company_id IS NULL OR company_id=NEW.company_id) AND deleted_at IS NULL FOR SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Asset Customer'; END IF;
  END IF;
  IF NEW.location_id IS NOT NULL THEN
   PERFORM 1 FROM locations WHERE id=NEW.location_id AND object_type='customer' AND object_id=NEW.customer_id AND (NEW.company_id IS NULL OR company_id=NEW.company_id) AND deleted_at IS NULL FOR SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Location belonging to the Asset Customer'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER asset_identity BEFORE INSERT OR UPDATE OR DELETE ON assets FOR EACH ROW EXECUTE FUNCTION asset_identity_guard();
CREATE TRIGGER master_asset_write_lock BEFORE INSERT OR UPDATE OR DELETE ON assets FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER asset_identity_evidence_immutable BEFORE UPDATE OR DELETE ON asset_identity_migration_evidence FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
