-- Import is performed under startup maintenance, after legacy journal recovery.
-- The completion fact is independent of policy data: deleting a policy cannot
-- turn a modern installation back into an uninitialized legacy installation.
CREATE TABLE restore_email_policy_import (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    complete boolean NOT NULL DEFAULT false
);
INSERT INTO restore_email_policy_import(singleton) VALUES(true);
CREATE TABLE restore_email_policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch uuid NOT NULL,
    enabled boolean NOT NULL,
    revision bigint NOT NULL CHECK (revision > 0),
    last_enable_operation uuid
);
ALTER TABLE online_save_actions ADD COLUMN execution_review text;
