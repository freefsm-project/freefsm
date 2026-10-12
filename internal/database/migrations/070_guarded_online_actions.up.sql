-- Request admission is durable before dispatch. It is deliberately separate from
-- domain commit evidence: an admitted request is NOT a committed domain effect.
CREATE TABLE online_save_actions (
    user_id bigint NOT NULL REFERENCES users(id),
    operation_id uuid NOT NULL,
    intent_hash text NOT NULL,
    method text NOT NULL,
    request_path text NOT NULL,
    review_path text NOT NULL,
    disposition text NOT NULL DEFAULT 'unknown' CHECK (disposition IN ('unknown','saved','review','rejected')),
    response_status integer,
    location text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    domain_committed_at timestamptz,
    PRIMARY KEY(user_id, operation_id)
);
CREATE TABLE online_save_effects (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id bigint NOT NULL,
    operation_id uuid NOT NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id bigint NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY(user_id, operation_id) REFERENCES online_save_actions(user_id, operation_id)
);
CREATE INDEX online_save_effects_action ON online_save_effects(user_id, operation_id);
CREATE FUNCTION protect_online_save_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'online action evidence is immutable';
END $$;
CREATE TRIGGER online_save_effects_immutable BEFORE UPDATE OR DELETE ON online_save_effects
FOR EACH ROW EXECUTE FUNCTION protect_online_save_evidence();
CREATE FUNCTION protect_online_save_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'online action identity is immutable'; END IF;
    IF NEW.user_id<>OLD.user_id OR NEW.operation_id<>OLD.operation_id
       OR NEW.intent_hash<>OLD.intent_hash OR NEW.method<>OLD.method
       OR NEW.request_path<>OLD.request_path OR NEW.review_path<>OLD.review_path
       OR NEW.created_at<>OLD.created_at OR OLD.completed_at IS NOT NULL
       OR (OLD.domain_committed_at IS NOT NULL AND NEW.domain_committed_at IS DISTINCT FROM OLD.domain_committed_at) THEN
        RAISE EXCEPTION 'online action identity and terminal disposition are immutable';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER online_save_identity_immutable BEFORE UPDATE OR DELETE ON online_save_actions
FOR EACH ROW EXECUTE FUNCTION protect_online_save_identity();
