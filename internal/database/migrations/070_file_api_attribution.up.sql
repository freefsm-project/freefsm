-- Optional client occurrence is publication intent, independent of transfer
-- completion or acceptance. Existing transfers/audit retain unknown history.
ALTER TABLE file_transfers ADD COLUMN occurred_at timestamptz;

-- Successful File commands (including desired-state no-ops) bind their operation
-- identity to the original device and intent. No credential material is audited.
CREATE TABLE file_mutation_operations (
    user_id bigint NOT NULL REFERENCES users(id),
    operation_id uuid NOT NULL,
    client_key text NOT NULL,
    intent_hash text NOT NULL,
    file_id bigint NOT NULL REFERENCES files(id),
    accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(user_id,operation_id)
);
CREATE FUNCTION guard_file_mutation_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'File operation evidence is immutable'; END $$;
CREATE TRIGGER file_mutation_operation_immutable BEFORE UPDATE OR DELETE ON file_mutation_operations
FOR EACH ROW EXECUTE FUNCTION guard_file_mutation_operation();
