-- Preserve existing product audit facts. A host operator is not a Product User
-- and exercises no product Role/Permission scope; do not impersonate the target.
ALTER TABLE authorization_audit RENAME COLUMN permission TO action;
ALTER TABLE authorization_audit ALTER COLUMN actor_id DROP NOT NULL;
ALTER TABLE authorization_audit ALTER COLUMN scope DROP NOT NULL;
ALTER TABLE authorization_audit ADD COLUMN actor_kind text NOT NULL DEFAULT 'product-user';
ALTER TABLE authorization_audit ADD COLUMN host_uid text;
ALTER TABLE authorization_audit ADD CONSTRAINT authorization_audit_attribution CHECK (
 (actor_kind='product-user' AND actor_id IS NOT NULL AND host_uid IS NULL
  AND scope IS NOT NULL AND scope IN ('A','J','C','O','U','S')
  AND channel<>'host-local' AND action<>'admin_password_recovered') OR
 (actor_kind='deployment-operator' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NOT NULL AND length(btrim(host_uid))>0
  AND channel='host-local' AND action='admin_password_recovered' AND target_type='user')
);
CREATE TRIGGER authorization_audit_no_truncate BEFORE TRUNCATE ON authorization_audit
 FOR EACH STATEMENT EXECUTE FUNCTION authorization_immutable();
