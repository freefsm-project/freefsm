-- Preserve the known cutover projection, not an invented original revision or
-- actor. Existing audit and clock evidence remain untouched.
ALTER TABLE online_save_actions ADD COLUMN transient_retry boolean NOT NULL DEFAULT false;
ALTER TABLE time_entries ADD COLUMN voided_at timestamptz;
ALTER TABLE time_entries ADD COLUMN job_title_snapshot text NOT NULL DEFAULT '';
-- This is the title known at cutover, never asserted to be the creation title.
UPDATE time_entries e SET job_title_snapshot=j.job_type FROM jobs j
 WHERE j.id=e.job_id AND j.company_id=e.company_id;
CREATE FUNCTION time_entry_job_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' OR NEW.job_id IS DISTINCT FROM OLD.job_id THEN
  NEW.job_title_snapshot := coalesce((SELECT job_type FROM jobs WHERE id=NEW.job_id AND company_id=NEW.company_id),'');
 ELSE
  NEW.job_title_snapshot := OLD.job_title_snapshot;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER time_entry_job_snapshot BEFORE INSERT OR UPDATE ON time_entries
 FOR EACH ROW EXECUTE FUNCTION time_entry_job_snapshot();
DROP INDEX time_entries_one_active_per_user;
CREATE UNIQUE INDEX time_entries_one_active_per_user ON time_entries(user_id)
 WHERE clock_out IS NULL AND voided_at IS NULL;
CREATE FUNCTION time_entry_void_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.voided_at IS NOT NULL THEN RAISE EXCEPTION 'Time Entry Void is terminal'; END IF;
 IF NEW.voided_at IS NOT NULL AND
  (NEW.clock_in IS DISTINCT FROM OLD.clock_in OR NEW.clock_out IS DISTINCT FROM OLD.clock_out
   OR NEW.job_id IS DISTINCT FROM OLD.job_id OR NEW.notes IS DISTINCT FROM OLD.notes) THEN
  RAISE EXCEPTION 'Void must retain the recorded time facts';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER time_entry_void_terminal BEFORE UPDATE ON time_entries
 FOR EACH ROW EXECUTE FUNCTION time_entry_void_terminal();
CREATE TABLE time_entry_history (
 id bigserial PRIMARY KEY,
 entry_id bigint NOT NULL REFERENCES time_entries(id),
 actor_id bigint REFERENCES users(id),
 actor_name text,
 accepted_at timestamptz NOT NULL,
 event text NOT NULL,
 reason text NOT NULL DEFAULT '',
 before_state jsonb,
 after_state jsonb NOT NULL
);
CREATE INDEX time_entry_history_entry ON time_entry_history(entry_id,id);
CREATE TRIGGER time_entry_history_immutable BEFORE UPDATE OR DELETE ON time_entry_history
 FOR EACH ROW EXECUTE FUNCTION clock_evidence_immutable();
INSERT INTO time_entry_history(entry_id,accepted_at,event,after_state)
 SELECT id,clock_timestamp(),'retained_at_upgrade',jsonb_build_object(
 'user_id',user_id,'clock_in',clock_in,'clock_out',clock_out,'job_id',job_id,'job_title',job_title_snapshot,'notes',notes)
 FROM time_entries;

-- Installed conflicts are retained and surfaced for explicit reasoned review.
-- Never manufacture an end or shift an instant merely to satisfy an invariant.
CREATE VIEW time_entry_review_required AS
 SELECT e.id FROM time_entries e WHERE e.voided_at IS NULL AND (
 e.clock_in > clock_timestamp() OR e.clock_out > clock_timestamp()
 OR e.clock_out <= e.clock_in
 OR EXISTS(SELECT 1 FROM time_entries other WHERE other.user_id=e.user_id
  AND other.id<>e.id AND other.voided_at IS NULL
  AND (e.clock_out IS NULL OR other.clock_in<e.clock_out)
  AND (other.clock_out IS NULL OR other.clock_out>e.clock_in)));
