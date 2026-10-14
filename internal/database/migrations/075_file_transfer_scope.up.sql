-- UUID collision behavior must not disclose another caller's transfers. Keep
-- every installed UUID, byte, parent and recorded origin; only scope the keys.
-- Chunk ownership is copied from its existing FK parent, never inferred from a
-- Role label, device label or historical account state.
ALTER TABLE file_transfer_chunks ADD COLUMN user_id bigint;
ALTER TABLE file_transfer_chunks ADD COLUMN session_hash text;
UPDATE file_transfer_chunks c SET user_id=t.user_id, session_hash=t.session_hash
FROM file_transfers t WHERE t.id=c.transfer_id;
ALTER TABLE file_transfer_chunks ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE file_transfer_chunks ALTER COLUMN session_hash SET NOT NULL;
ALTER TABLE file_transfer_chunks DROP CONSTRAINT file_transfer_chunks_transfer_id_fkey;
ALTER TABLE file_transfer_chunks DROP CONSTRAINT file_transfer_chunks_pkey;
ALTER TABLE file_transfers DROP CONSTRAINT file_transfers_pkey;
ALTER TABLE file_transfers ADD PRIMARY KEY (user_id,session_hash,id);
ALTER TABLE file_transfer_chunks ADD PRIMARY KEY (user_id,session_hash,transfer_id,offset_bytes);
ALTER TABLE file_transfer_chunks ADD CONSTRAINT file_transfer_chunks_transfer_fkey
FOREIGN KEY (user_id,session_hash,transfer_id) REFERENCES file_transfers(user_id,session_hash,id);
CREATE INDEX file_transfer_scoped_digest ON file_transfers
(user_id,session_hash,object_type,object_id,sha256,created_at DESC,id) WHERE transferred=size;
