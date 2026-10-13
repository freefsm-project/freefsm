-- New protocol records only: no installed account, Role, grant or session rewrite.
CREATE TABLE device_pairings (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 operation_id uuid NOT NULL UNIQUE,
 device_hash text NOT NULL UNIQUE,
 code_hash text NOT NULL UNIQUE,
 device_name text NOT NULL CHECK (length(device_name) BETWEEN 1 AND 100),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT (clock_timestamp()+interval '10 minutes'),
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','approved','cancelled','redeemed')),
 review_hash text UNIQUE,
 browser_hash text,
 user_id bigint REFERENCES users(id),
 credential_revision bigint,
 session_id bigint REFERENCES sessions(id) ON DELETE SET NULL,
 redeem_operation_id uuid UNIQUE,
 cancel_operation_id uuid UNIQUE,
 approved_at timestamptz,
 consumed_at timestamptz
);

-- Preserve existing attribution rules; add only these public protocol actions.
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
   (channel='pairing' AND action IN ('user.pairing.start','user.pairing.cancel') AND target_type='device_pairing')))
);
