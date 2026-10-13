-- Current identities acquire non-reused evidence keys. Do NOT backfill old audit
-- rows from today's users/Roles: their event-time names/identities are unknown.
ALTER TABLE users ADD COLUMN audit_identity uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE authorization_roles ADD COLUMN audit_identity uuid NOT NULL DEFAULT gen_random_uuid() UNIQUE;
ALTER TABLE authorization_audit
 ADD COLUMN actor_identity uuid,
 ADD COLUMN actor_name text,
 ADD COLUMN role_identity uuid,
 ADD COLUMN role_name text,
 ADD COLUMN device_name text,
 ADD COLUMN operation_id uuid,
 ADD COLUMN occurred_at timestamptz,
 ADD COLUMN session_id bigint;

-- PostgreSQL accepts relative timestamp input such as 'now'. That is not a
-- recorded occurrence instant and must never acquire a new meaning on reads.
CREATE FUNCTION authorization_audit_instant(value text) RETURNS timestamptz
 LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
 IF value ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}([.][0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})$'
    AND pg_input_is_valid(value,'timestamp with time zone') THEN
  RETURN value::timestamptz;
 END IF;
 RETURN NULL;
END $$;

CREATE FUNCTION authorization_capture_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.actor_kind='product-user' THEN
  SELECT audit_identity,name INTO NEW.actor_identity,NEW.actor_name FROM users WHERE id=NEW.actor_id;
 END IF;
 SELECT audit_identity,name INTO NEW.role_identity,NEW.role_name FROM authorization_roles WHERE id=NEW.role_id;
 IF pg_input_is_valid(NEW.detail->>'operation_id','uuid') THEN
  NEW.operation_id := (NEW.detail->>'operation_id')::uuid;
 END IF;
 NEW.occurred_at := coalesce(NEW.occurred_at,authorization_audit_instant(NEW.detail->>'occurred_at'));
 IF NEW.channel='api' AND pg_input_is_valid(NEW.detail->>'session_id','bigint') THEN
  NEW.session_id := (NEW.detail->>'session_id')::bigint;
  SELECT device_name INTO NEW.device_name FROM sessions WHERE id=NEW.session_id AND user_id=NEW.actor_id AND kind='mobile';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER authorization_capture_evidence BEFORE INSERT ON authorization_audit
 FOR EACH ROW EXECUTE FUNCTION authorization_capture_evidence();
CREATE INDEX authorization_audit_filters ON authorization_audit(action,scope,channel,outcome,id);
