-- Only installed owning commands opt in. Existing request history is preserved.
ALTER TABLE online_save_actions
    ADD COLUMN execution_token uuid,
    ADD COLUMN execution_fenced boolean NOT NULL DEFAULT false;

ALTER TABLE online_save_actions ADD CONSTRAINT online_execution_fence_identity
    CHECK (NOT execution_fenced OR execution_token IS NOT NULL);
