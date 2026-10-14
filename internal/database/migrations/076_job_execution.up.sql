-- Retain the pre-v1 current bytes, identifiers and recorded activity. A baseline
-- is an observation at migration, never an invented historical revision/author.
ALTER TABLE jobs ADD COLUMN notes_revision uuid NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE jobs ADD COLUMN planning_revision uuid NOT NULL DEFAULT gen_random_uuid();
CREATE FUNCTION job_planning_revision() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF (to_jsonb(NEW)-ARRAY['updated_at','status_id','status_revision','tech_notes','notes_revision','planning_revision'])
 IS DISTINCT FROM (to_jsonb(OLD)-ARRAY['updated_at','status_id','status_revision','tech_notes','notes_revision','planning_revision']) THEN
  NEW.planning_revision=gen_random_uuid();
 END IF;
 RETURN NEW;
 END $$;
CREATE TRIGGER zz_job_planning_revision BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION job_planning_revision();
-- Contact membership is part of planning but lives outside the Job row. Keep
-- its existing stale/ABA protection when execution stops changing planning's
-- token. The compatibility singular Contact is not the complete membership.
CREATE FUNCTION job_contact_planning_revision() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='UPDATE' AND NEW IS NOT DISTINCT FROM OLD THEN RETURN NEW; END IF;
 IF TG_OP<>'INSERT' THEN
  UPDATE jobs SET planning_revision=gen_random_uuid() WHERE id=OLD.job_id;
 END IF;
 IF TG_OP='INSERT' OR (TG_OP='UPDATE' AND NEW.job_id IS DISTINCT FROM OLD.job_id) THEN
  UPDATE jobs SET planning_revision=gen_random_uuid() WHERE id=NEW.job_id;
 END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
 END $$;
CREATE TRIGGER job_contact_planning_revision AFTER INSERT OR UPDATE OR DELETE ON job_contacts FOR EACH ROW EXECUTE FUNCTION job_contact_planning_revision();
CREATE TABLE job_notes_history (
 revision uuid PRIMARY KEY,
 job_id bigint NOT NULL REFERENCES jobs(id),
 content text NOT NULL,
 actor_id bigint REFERENCES users(id),
 accepted_at timestamptz,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 baseline boolean NOT NULL DEFAULT false,
 baseline_source text NOT NULL DEFAULT '',
 CHECK (baseline OR (actor_id IS NOT NULL AND accepted_at IS NOT NULL))
);
CREATE TABLE job_notes_reviews (
 job_id bigint PRIMARY KEY REFERENCES jobs(id),
 baseline_revision uuid NOT NULL REFERENCES job_notes_history(revision),
 reviewer_id bigint REFERENCES users(id),
 reviewed_at timestamptz,
 reason text NOT NULL DEFAULT ''
);
CREATE FUNCTION job_notes_review_once() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF TG_OP='DELETE' OR OLD.reviewed_at IS NOT NULL OR NEW.job_id<>OLD.job_id OR NEW.baseline_revision<>OLD.baseline_revision THEN
  RAISE EXCEPTION 'Recorded Field Notes baseline review is immutable';
 END IF;
 RETURN NEW;
 END $$;
CREATE TRIGGER job_notes_review_once BEFORE UPDATE OR DELETE ON job_notes_reviews FOR EACH ROW EXECUTE FUNCTION job_notes_review_once();
INSERT INTO job_notes_history(revision,job_id,content,baseline,baseline_source)
 SELECT notes_revision,id,coalesce(tech_notes,''),true,'migration' FROM jobs;
INSERT INTO job_notes_reviews(job_id,baseline_revision)
 SELECT id,notes_revision FROM jobs WHERE coalesce(tech_notes,'')<>'';
CREATE FUNCTION job_execution_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN RAISE EXCEPTION 'Job execution history and terminal receipts are immutable'; END $$;
CREATE TRIGGER job_notes_history_immutable BEFORE UPDATE OR DELETE ON job_notes_history
 FOR EACH ROW EXECUTE FUNCTION job_execution_immutable();
CREATE FUNCTION job_notes_guard() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 IF NEW.tech_notes IS DISTINCT FROM OLD.tech_notes THEN
  IF current_setting('freefsm.field_notes',true) IS DISTINCT FROM 'allowed' THEN
   RAISE EXCEPTION 'Field Notes require the independent reviewed command';
  END IF;
  NEW.notes_revision=gen_random_uuid();
 END IF;
 RETURN NEW;
 END $$;
CREATE TRIGGER job_notes_guard BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION job_notes_guard();
CREATE FUNCTION job_notes_initial() RETURNS trigger LANGUAGE plpgsql AS $$
 BEGIN
 INSERT INTO job_notes_history(revision,job_id,content,baseline,baseline_source) VALUES(NEW.notes_revision,NEW.id,coalesce(NEW.tech_notes,''),true,'initial');
 IF coalesce(NEW.tech_notes,'')<>'' THEN
  INSERT INTO job_notes_reviews(job_id,baseline_revision) VALUES(NEW.id,NEW.notes_revision);
 END IF;
 RETURN NEW;
 END $$;
CREATE TRIGGER job_notes_initial AFTER INSERT ON jobs FOR EACH ROW EXECUTE FUNCTION job_notes_initial();
CREATE TABLE job_execution_receipts (
 user_id bigint NOT NULL REFERENCES users(id),
 operation_id uuid NOT NULL,
 client_key text NOT NULL,
 intent_hash text NOT NULL,
 code text NOT NULL,
 effect_id uuid,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(user_id,operation_id)
);
CREATE TRIGGER job_execution_receipts_immutable BEFORE UPDATE OR DELETE ON job_execution_receipts
 FOR EACH ROW EXECUTE FUNCTION job_execution_immutable();
