-- Preserve every existing identity and recorded fact. NULL digest means legacy
-- bytes have not been verified; it is deliberately not invented upload history.
ALTER TABLE files ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK(revision > 0);
ALTER TABLE files ADD COLUMN removed_at timestamptz;
ALTER TABLE files ADD COLUMN removed_by bigint;
ALTER TABLE files ADD COLUMN sha256 text;
ALTER TABLE files ADD CONSTRAINT file_removal_attribution CHECK ((removed_at IS NULL) = (removed_by IS NULL));

CREATE FUNCTION advance_file_parent_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.object_type IS DISTINCT FROM OLD.object_type OR NEW.object_id IS DISTINCT FROM OLD.object_id THEN
        NEW.revision := OLD.revision + 1;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER file_parent_revision BEFORE UPDATE OF object_type,object_id ON files
FOR EACH ROW EXECUTE FUNCTION advance_file_parent_revision();

CREATE TABLE file_publications (
    user_id bigint NOT NULL REFERENCES users(id),
    operation_id uuid NOT NULL,
    intent text NOT NULL,
    file_id bigint NOT NULL REFERENCES files(id),
    PRIMARY KEY(user_id, operation_id)
);

-- Incomplete transfers are not attachments. Chunks are bounded, transactional,
-- and backed up with the database; publication alone creates a File identity.
CREATE TABLE file_transfers (
    id uuid PRIMARY KEY,
    user_id bigint NOT NULL REFERENCES users(id),
    session_hash text NOT NULL,
    publication_id uuid NOT NULL,
    object_type text NOT NULL,
    object_id bigint NOT NULL,
    name text NOT NULL,
    size bigint NOT NULL CHECK(size > 0),
    sha256 text NOT NULL CHECK(sha256 ~ '^[0-9a-f]{64}$'),
    transferred bigint NOT NULL DEFAULT 0 CHECK(transferred >= 0 AND transferred <= size),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE file_transfer_chunks (
    transfer_id uuid NOT NULL REFERENCES file_transfers(id),
    offset_bytes bigint NOT NULL CHECK(offset_bytes >= 0),
    data bytea NOT NULL CHECK(octet_length(data) BETWEEN 1 AND 1048576),
    PRIMARY KEY(transfer_id, offset_bytes)
);
