-- Preserve every existing ID, author, byte of text and timestamp. Existing
-- installations had no reply/removal fields; NULL records absence of knowledge.
ALTER TABLE comments ADD COLUMN reply_to BIGINT REFERENCES comments(id);
ALTER TABLE comments ADD COLUMN removed_at TIMESTAMPTZ;
ALTER TABLE comments ADD COLUMN removed_by BIGINT REFERENCES users(id);
CREATE INDEX comments_thread_order ON comments(company_id,object_type,object_id,created_at,id);

-- Intent is a digest, never a second ordinary copy of removed text. Receipts
-- retain nonsensitive effect identity even after the parent is no longer visible.
CREATE TABLE comment_operation_receipts (
 user_id BIGINT NOT NULL REFERENCES users(id),
 operation_id UUID NOT NULL,
 client_key TEXT NOT NULL,
 intent_hash TEXT NOT NULL,
 comment_id BIGINT,
 code TEXT NOT NULL,
 accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(user_id,operation_id)
);
CREATE FUNCTION comment_receipt_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Comment receipts are immutable'; END $$;
CREATE TRIGGER comment_receipt_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON comment_operation_receipts FOR EACH STATEMENT EXECUTE FUNCTION comment_receipt_immutable();
