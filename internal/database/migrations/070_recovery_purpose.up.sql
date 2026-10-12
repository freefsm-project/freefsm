-- Retain unknown historical links verbatim for operator review, never validate
-- them as newly issued recovery credentials. No user/hash/history is rewritten.
ALTER TABLE password_reset_tokens ADD COLUMN purpose text NOT NULL DEFAULT 'legacy-unknown';
ALTER TABLE users ADD COLUMN credential_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE password_reset_tokens ADD COLUMN credential_revision bigint;
ALTER TABLE password_reset_tokens ADD COLUMN recipient text;
CREATE VIEW password_recovery_review AS
 SELECT id,user_id,company_id,created_at,expires_at,'Unknown legacy purpose or validity; replace through recovery'::text AS review_reason
 FROM password_reset_tokens WHERE purpose <> 'password-recovery-v1';
CREATE UNIQUE INDEX newest_password_recovery ON password_reset_tokens(user_id) WHERE purpose='password-recovery-v1';

CREATE FUNCTION advance_credential_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.password_hash IS DISTINCT FROM OLD.password_hash OR NEW.email IS DISTINCT FROM OLD.email
    OR NEW.is_active IS DISTINCT FROM OLD.is_active OR NEW.force_password_change IS DISTINCT FROM OLD.force_password_change THEN
  NEW.credential_revision := OLD.credential_revision+1;
  DELETE FROM password_reset_tokens WHERE user_id=OLD.id AND purpose='password-recovery-v1';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER credential_revision BEFORE UPDATE OF password_hash,email,is_active,force_password_change ON users
 FOR EACH ROW EXECUTE FUNCTION advance_credential_revision();

-- Every address follows the same durable dispatch path. No bearer token or
-- password is persisted here. A resumed claim issues a fresh newest-only proof.
CREATE TABLE password_recovery_requests (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 email text NOT NULL,
 user_id bigint REFERENCES users(id) ON DELETE CASCADE,
 credential_revision bigint,
 base_url text NOT NULL,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 claimed_until timestamptz,
 completed_at timestamptz
);
CREATE INDEX pending_password_recovery_requests ON password_recovery_requests(id) WHERE completed_at IS NULL;

CREATE VIEW password_policy_review AS
 SELECT company_id,password_min_length,'Choose a minimum between 6 and 72 in Settings / Advanced security; existing passwords remain valid'::text AS review_reason
 FROM company_settings WHERE password_min_length<6 OR password_min_length>72;

ALTER TABLE authorization_audit DROP CONSTRAINT authorization_audit_attribution;
ALTER TABLE authorization_audit ADD CONSTRAINT authorization_audit_attribution CHECK (
 (actor_kind='product-user' AND actor_id IS NOT NULL AND host_uid IS NULL
  AND scope IS NOT NULL AND scope IN ('A','J','C','O','U','S')
  AND channel<>'host-local' AND action<>'admin_password_recovered') OR
 (actor_kind='deployment-operator' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NOT NULL AND length(btrim(host_uid))>0
  AND channel='host-local' AND action='admin_password_recovered' AND target_type='user') OR
 (actor_kind='anonymous-request' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NULL AND channel='recovery'
  AND action='user.recovery.request' AND target_type='user')
);
