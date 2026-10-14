-- Observations are not acquisitions or transfers. In particular created_at is
-- not an effective ownership date. Preserve unknown dates explicitly.
CREATE TABLE asset_association_migration_evidence (
 snapshot text PRIMARY KEY, evidence jsonb NOT NULL, reviewer text NOT NULL,
 reason text NOT NULL, reviewed_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
DO $$ DECLARE review jsonb := nullif(current_setting('freefsm.asset_association_review',true),'')::jsonb;
BEGIN
 IF EXISTS(SELECT 1 FROM assets) THEN
  IF review IS NULL OR review->>'administrator_reviewed'<>'true' OR review->>'retain_observed_unknown_dates'<>'true' THEN RAISE EXCEPTION 'Asset association cutover requires Administrator review; observed facts are unchanged'; END IF;
  INSERT INTO asset_association_migration_evidence VALUES(review->>'snapshot',review->'evidence',review->>'reviewer',review->>'reason',clock_timestamp());
 END IF;
END $$;
CREATE TRIGGER asset_association_migration_evidence_immutable BEFORE UPDATE OR DELETE ON asset_association_migration_evidence FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
CREATE TABLE asset_association_origins (
 asset_id bigint PRIMARY KEY REFERENCES assets(id), customer_id bigint REFERENCES customers(id),
 location_id bigint REFERENCES locations(id), observed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 legacy boolean NOT NULL, evidence jsonb NOT NULL
);
INSERT INTO asset_association_origins(asset_id,customer_id,location_id,legacy,evidence)
 SELECT id,customer_id,location_id,true,to_jsonb(a) FROM assets a;
CREATE TRIGGER asset_association_origins_immutable BEFORE UPDATE OR DELETE ON asset_association_origins FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
CREATE FUNCTION asset_association_origin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO asset_association_origins(asset_id,customer_id,location_id,legacy,evidence)
 VALUES(NEW.id,NEW.customer_id,NEW.location_id,false,to_jsonb(NEW));
 RETURN NEW;
END $$;
CREATE TRIGGER asset_association_origin AFTER INSERT ON assets FOR EACH ROW EXECUTE FUNCTION asset_association_origin();

CREATE TABLE asset_association_events (
 id bigserial PRIMARY KEY, asset_id bigint NOT NULL REFERENCES assets(id),
 event_id bigint REFERENCES asset_association_events(id),
 action text NOT NULL CHECK(action IN ('transfer','place','return','correct')),
 effective_date date NOT NULL, customer_id bigint REFERENCES customers(id), location_id bigint REFERENCES locations(id),
 reason text NOT NULL CHECK(btrim(reason)<>''), actor_id bigint NOT NULL REFERENCES users(id),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(), before_fact jsonb NOT NULL,
 CHECK((action='correct')=(event_id IS NOT NULL))
);
CREATE TRIGGER asset_association_events_immutable BEFORE UPDATE OR DELETE ON asset_association_events FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
CREATE VIEW asset_association_facts AS
 SELECT DISTINCT ON (coalesce(event_id,id)) coalesce(event_id,id) AS event_id,id AS revision_id,asset_id,effective_date,customer_id,location_id
 FROM asset_association_events ORDER BY coalesce(event_id,id),id DESC;
CREATE VIEW asset_association_periods AS
 SELECT asset_id,event_id,customer_id,location_id,effective_date AS starts_on,
 lead(effective_date) OVER(PARTITION BY asset_id ORDER BY effective_date NULLS FIRST,event_id) AS ends_before
 FROM (SELECT asset_id,0::bigint AS event_id,customer_id,location_id,NULL::date AS effective_date FROM asset_association_origins
 UNION ALL SELECT asset_id,event_id,customer_id,location_id,effective_date FROM asset_association_facts) p;

CREATE TABLE job_asset_links (
 id bigserial PRIMARY KEY, job_id bigint NOT NULL REFERENCES jobs(id), asset_id bigint NOT NULL REFERENCES assets(id),
 customer_id bigint NOT NULL REFERENCES customers(id), number text NOT NULL, name text NOT NULL,
 linked_on date, association_event_id bigint NOT NULL DEFAULT 0, recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 operational boolean NOT NULL, unlinked_at timestamptz,
 legacy boolean NOT NULL DEFAULT false, review_reason text NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX job_asset_current_link ON job_asset_links(job_id,asset_id) WHERE operational AND unlinked_at IS NULL;
INSERT INTO job_asset_links(job_id,asset_id,customer_id,number,name,operational,legacy,review_reason)
 SELECT j.id,a.id,j.customer_id,a.number,CASE WHEN a.customer_id=j.customer_id AND a.company_id=j.company_id THEN a.name ELSE 'Historical Asset (original name unknown)' END,coalesce(a.customer_id=j.customer_id AND a.company_id=j.company_id,false),true,
 CASE WHEN a.customer_id IS DISTINCT FROM j.customer_id OR a.company_id IS DISTINCT FROM j.company_id THEN 'Cross-Customer link; historical, non-authorizing; dates and transitions unknown' ELSE 'Historical link date and earlier transitions unknown' END
 FROM jobs j JOIN assets a ON a.id=j.asset_id;

CREATE OR REPLACE FUNCTION asset_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Asset identity is retained; archive instead'; END IF;
 IF TG_OP='UPDATE' THEN
  IF NEW.number IS DISTINCT FROM OLD.number OR NEW.ownership_class IS DISTINCT FROM OLD.ownership_class THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Asset Number and ownership class are immutable';
  END IF;
  IF NEW.customer_id IS DISTINCT FROM OLD.customer_id AND current_setting('freefsm.asset_association',true) IS DISTINCT FROM 'allowed' THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Use an explicit Asset association action';
  END IF;
  IF OLD.deleted_at IS NULL AND NEW.deleted_at IS NOT NULL AND (NEW.ownership_class='company_owned' AND NEW.customer_id IS NOT NULL OR EXISTS(
    SELECT 1 FROM job_asset_links l JOIN jobs j ON j.id=l.job_id LEFT JOIN statuses s ON s.id=j.status_id WHERE l.asset_id=NEW.id AND l.operational AND l.unlinked_at IS NULL AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled'))) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Asset has an active placement or unfinished Job';
  END IF;
 END IF;
 IF TG_OP='INSERT' OR NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.location_id IS DISTINCT FROM OLD.location_id OR (OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NULL) THEN
  IF NEW.customer_id IS NOT NULL THEN
   PERFORM 1 FROM customers WHERE id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL FOR SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Asset Customer'; END IF;
  END IF;
  IF NEW.location_id IS NOT NULL THEN
   PERFORM 1 FROM locations WHERE id=NEW.location_id AND object_type='customer' AND object_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL FOR SHARE;
   IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Location belonging to the Asset Customer'; END IF;
  END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION job_asset_link_mirror() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN
  IF NEW.asset_id IS NOT DISTINCT FROM OLD.asset_id AND NEW.customer_id=OLD.customer_id THEN RETURN NEW; END IF;
  UPDATE job_asset_links SET operational=false,unlinked_at=clock_timestamp() WHERE job_id=NEW.id AND unlinked_at IS NULL AND (NEW.customer_id<>OLD.customer_id OR (asset_id=OLD.asset_id AND operational));
 END IF;
 IF NEW.asset_id IS NOT NULL THEN
  PERFORM 1 FROM assets WHERE id=NEW.asset_id AND customer_id=NEW.customer_id AND company_id=NEW.company_id AND deleted_at IS NULL FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active Asset belonging to the Job Customer'; END IF;
  INSERT INTO job_asset_links(job_id,asset_id,customer_id,number,name,linked_on,association_event_id,operational)
   SELECT NEW.id,id,NEW.customer_id,number,name,(clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=NEW.company_id),'UTC'))::date,coalesce((SELECT event_id FROM asset_association_facts WHERE asset_id=NEW.asset_id ORDER BY effective_date DESC,event_id DESC LIMIT 1),0),true FROM assets WHERE id=NEW.asset_id;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_asset_link_mirror AFTER INSERT OR UPDATE OF asset_id,customer_id ON jobs FOR EACH ROW EXECUTE FUNCTION job_asset_link_mirror();

CREATE FUNCTION job_asset_link_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'Job Asset evidence is retained; unlink instead'; END IF;
 IF TG_OP='UPDATE' THEN
  IF (to_jsonb(NEW)-ARRAY['operational','unlinked_at']) IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['operational','unlinked_at']) OR (NOT OLD.operational AND NEW.operational) OR (OLD.unlinked_at IS NOT NULL AND NEW.unlinked_at IS DISTINCT FROM OLD.unlinked_at) THEN RAISE EXCEPTION 'Job Asset evidence is immutable'; END IF;
 ELSE
  IF NOT EXISTS(SELECT 1 FROM jobs j JOIN assets a ON a.id=NEW.asset_id AND a.customer_id=j.customer_id AND a.company_id=j.company_id WHERE j.id=NEW.job_id AND j.customer_id=NEW.customer_id AND j.deleted_at IS NULL AND a.deleted_at IS NULL) THEN RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Select an active same-Customer Job Asset'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_asset_link_guard BEFORE INSERT OR UPDATE OR DELETE ON job_asset_links FOR EACH ROW EXECUTE FUNCTION job_asset_link_guard();
CREATE TRIGGER job_asset_link_write_lock BEFORE INSERT OR UPDATE OR DELETE ON job_asset_links FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
