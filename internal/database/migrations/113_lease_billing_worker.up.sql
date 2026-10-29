-- Preserve every existing obligation, generation, Invoice and permission.
CREATE TABLE lease_billing_worker_state (
 lease_id bigint PRIMARY KEY REFERENCES lease_finalizations(lease_id),
 attempted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 failure text NOT NULL DEFAULT ''
);

ALTER TABLE authorization_audit DROP CONSTRAINT authorization_audit_attribution;
ALTER TABLE authorization_audit ADD CONSTRAINT authorization_audit_attribution CHECK (
 (actor_kind='product-user' AND actor_id IS NOT NULL AND host_uid IS NULL
  AND scope IS NOT NULL AND scope IN ('A','J','C','O','U','S')
  AND channel<>'host-local' AND action<>'admin_password_recovered') OR
 (actor_kind='deployment-operator' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NOT NULL AND length(btrim(host_uid))>0
  AND channel='host-local' AND action='admin_password_recovered' AND target_type='user') OR
 (actor_kind='anonymous-request' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NULL AND (
   (channel='recovery' AND action='user.recovery.request' AND target_type='user') OR
   (channel='pairing' AND action IN ('user.pairing.start','user.pairing.cancel') AND target_type='device_pairing'))) OR
 (actor_kind='lease-billing-worker' AND actor_id IS NULL AND role_id IS NULL
  AND scope IS NULL AND host_uid IS NULL AND channel='worker'
  AND action='lease.invoice.generate' AND target_type='invoice'
  AND detail ? 'obligation_id')
);
