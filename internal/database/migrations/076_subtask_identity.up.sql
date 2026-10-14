-- The original JSON is evidence, not an attributable completion event. Inventory
-- it without rewriting a byte or assigning historical actors/timestamps.
CREATE TABLE subtask_legacy_reviews (
    job_id bigint PRIMARY KEY REFERENCES jobs(id),
    recorded jsonb NOT NULL,
    reviewed_by bigint REFERENCES users(id),
    reviewed_at timestamptz,
    mapping jsonb,
    CHECK ((reviewed_by IS NULL) = (reviewed_at IS NULL))
);
INSERT INTO subtask_legacy_reviews(job_id,recorded)
SELECT id,subtasks FROM jobs WHERE subtasks <> '[]'::jsonb;

CREATE TABLE job_subtasks (
    id uuid PRIMARY KEY,
    job_id bigint NOT NULL REFERENCES jobs(id),
    title text NOT NULL CHECK (btrim(title) <> ''),
    sort_order integer NOT NULL,
    completed boolean NOT NULL DEFAULT false,
    completion_revision uuid NOT NULL,
    completion_actor bigint REFERENCES users(id),
    completion_at timestamptz,
    removed_at timestamptz,
    legacy_position integer,
    CHECK ((completion_actor IS NULL) = (completion_at IS NULL)),
    UNIQUE(job_id,legacy_position)
);
CREATE INDEX job_subtasks_order ON job_subtasks(job_id,sort_order,id) WHERE removed_at IS NULL;
CREATE TABLE subtask_history (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id bigint NOT NULL REFERENCES jobs(id),
    subtask_id uuid REFERENCES job_subtasks(id),
    actor_id bigint NOT NULL REFERENCES users(id),
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    action text NOT NULL,
    before_value jsonb,
    after_value jsonb,
    operation_id uuid NOT NULL
);
CREATE TABLE subtask_operation_receipts (
    user_id bigint NOT NULL REFERENCES users(id),
    operation_id uuid NOT NULL,
    client_key text NOT NULL,
    intent_hash text NOT NULL,
    subtask_id uuid,
    code text NOT NULL,
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(user_id,operation_id)
);
CREATE FUNCTION preserve_subtask_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Subtask history and receipts are immutable'; END $$;
CREATE TRIGGER subtask_history_immutable BEFORE UPDATE OR DELETE ON subtask_history
FOR EACH ROW EXECUTE FUNCTION preserve_subtask_evidence();
CREATE TRIGGER subtask_receipt_immutable BEFORE UPDATE OR DELETE ON subtask_operation_receipts
FOR EACH ROW EXECUTE FUNCTION preserve_subtask_evidence();
CREATE FUNCTION preserve_subtask_review() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' OR OLD.reviewed_at IS NOT NULL OR NEW.job_id<>OLD.job_id OR NEW.recorded<>OLD.recorded THEN
        RAISE EXCEPTION 'Recorded Subtask review evidence is immutable';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER subtask_review_immutable BEFORE UPDATE OR DELETE ON subtask_legacy_reviews
FOR EACH ROW EXECUTE FUNCTION preserve_subtask_review();
