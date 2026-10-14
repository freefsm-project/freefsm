CREATE TABLE commercial_client_identities (
 client_id uuid PRIMARY KEY,
 document_type text NOT NULL CHECK(document_type IN ('estimate','invoice')),
 document_id bigint NOT NULL,
 user_id bigint NOT NULL REFERENCES users(id),
 client_hash text NOT NULL,
 UNIQUE(document_type,document_id)
);
CREATE TABLE commercial_operations (
 user_id bigint NOT NULL REFERENCES users(id),
 client_hash text NOT NULL,
 operation_id uuid NOT NULL,
 document_type text NOT NULL CHECK(document_type IN ('estimate','invoice')),
 requested_id bigint NOT NULL,
 intent jsonb NOT NULL,
 receipt jsonb NOT NULL,
 accepted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(user_id,client_hash,operation_id)
);
CREATE FUNCTION commercial_operation_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Commercial operation evidence is immutable'; END $$;
CREATE TRIGGER commercial_operation_immutable BEFORE UPDATE OR DELETE ON commercial_operations FOR EACH ROW EXECUTE FUNCTION commercial_operation_immutable();
CREATE TRIGGER commercial_identity_immutable BEFORE UPDATE OR DELETE ON commercial_client_identities FOR EACH ROW EXECUTE FUNCTION commercial_operation_immutable();
