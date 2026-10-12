-- Online browser receipts contain no submitted field values. They commit in the
-- owning command transaction and remain queryable after resource permission loss.
CREATE TABLE web_action_receipts (
    user_id bigint NOT NULL REFERENCES users(id),
    operation_id uuid NOT NULL,
    intent_hash text NOT NULL,
    action text NOT NULL,
    effect_id bigint NOT NULL,
    disposition text NOT NULL CHECK(disposition IN ('saved','conflict','invalid')),
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(user_id,operation_id)
);
CREATE FUNCTION immutable_web_action_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'web action receipts are immutable'; END $$;
CREATE TRIGGER web_action_receipts_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON web_action_receipts
FOR EACH STATEMENT EXECUTE FUNCTION immutable_web_action_receipt();
