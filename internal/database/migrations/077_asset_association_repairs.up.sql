-- Reconcile the legacy single-selection editor with canonical multiple links.
-- Selecting an existing operational link is a projection change, not a new
-- visit or an implicit removal of another explicit link.
CREATE OR REPLACE FUNCTION job_asset_link_mirror() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE existing_link boolean;
BEGIN
 existing_link := EXISTS(SELECT 1 FROM job_asset_links WHERE job_id=NEW.id AND asset_id=NEW.asset_id AND customer_id=NEW.customer_id AND operational AND unlinked_at IS NULL);
 IF TG_OP='UPDATE' THEN
  IF NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id AND NEW.customer_id=OLD.customer_id THEN RETURN NEW; END IF;
  UPDATE job_asset_links SET operational=false,unlinked_at=clock_timestamp()
   WHERE job_id=NEW.id AND unlinked_at IS NULL AND
   (NEW.customer_id<>OLD.customer_id OR (NOT existing_link AND asset_id=OLD.asset_id AND operational));
 END IF;
 IF NEW.asset_id IS NOT NULL THEN
  PERFORM 1 FROM assets WHERE id=NEW.asset_id AND customer_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Asset belonging to the Job Customer'; END IF;
  IF NOT existing_link THEN
   INSERT INTO job_asset_links(job_id,asset_id,customer_id,number,name,linked_on,association_event_id,operational)
    SELECT NEW.id,id,NEW.customer_id,number,name,(clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=NEW.company_id),'UTC'))::date,coalesce((SELECT event_id FROM asset_association_facts WHERE asset_id=NEW.asset_id ORDER BY effective_date DESC,event_id DESC LIMIT 1),0),true FROM assets WHERE id=NEW.asset_id;
  END IF;
 END IF;
 RETURN NEW;
END $$;

-- Factual revisions of the initial observation are not moves. Their mandatory
-- as-of date records the correction's effective assertion, not an invented
-- acquisition/start date. The original observation and its unknown start stay.
CREATE TABLE asset_association_origin_revisions (
 id bigint PRIMARY KEY DEFAULT nextval('asset_association_events_id_seq'),
 asset_id bigint NOT NULL REFERENCES asset_association_origins(asset_id),
 effective_date date NOT NULL, customer_id bigint REFERENCES customers(id),
 location_id bigint REFERENCES locations(id),
 reason text NOT NULL CHECK(btrim(reason)<>''), actor_id bigint NOT NULL REFERENCES users(id),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(), before_fact jsonb NOT NULL
);
CREATE TRIGGER asset_association_origin_revisions_immutable BEFORE UPDATE OR DELETE ON asset_association_origin_revisions FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
CREATE VIEW asset_association_origin_facts AS
 SELECT o.asset_id,
 CASE WHEN r.id IS NULL THEN o.customer_id ELSE r.customer_id END AS customer_id,
 CASE WHEN r.id IS NULL THEN o.location_id ELSE r.location_id END AS location_id,
 o.observed_at,o.legacy,o.evidence,r.id AS revision_id
 FROM asset_association_origins o LEFT JOIN LATERAL
 (SELECT * FROM asset_association_origin_revisions WHERE asset_id=o.asset_id ORDER BY id DESC LIMIT 1) r ON true;
CREATE OR REPLACE VIEW asset_association_periods AS
 SELECT asset_id,event_id,customer_id,location_id,effective_date AS starts_on,
 lead(effective_date) OVER(PARTITION BY asset_id ORDER BY effective_date NULLS FIRST,event_id) AS ends_before
 FROM (SELECT asset_id,0::bigint AS event_id,customer_id,location_id,NULL::date AS effective_date FROM asset_association_origin_facts
 UNION ALL SELECT asset_id,event_id,customer_id,location_id,effective_date FROM asset_association_facts) p;
