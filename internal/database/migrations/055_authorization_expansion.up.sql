-- Expand only. Legacy role values and all existing identities remain recorded.
CREATE TABLE authorization_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    ready boolean NOT NULL,
    fresh_install boolean NOT NULL
);
INSERT INTO authorization_state SELECT true, NOT EXISTS(SELECT 1 FROM users), NOT EXISTS(SELECT 1 FROM users);
ALTER TABLE authorization_state ADD COLUMN bootstrapped boolean NOT NULL DEFAULT false;
CREATE TABLE authorization_legacy_approvers(user_id bigint PRIMARY KEY REFERENCES users(id));
INSERT INTO authorization_legacy_approvers SELECT id FROM users WHERE role='admin';

CREATE TABLE authorization_roles (
    id bigserial PRIMARY KEY,
    name text NOT NULL CHECK(length(btrim(name))>0),
    protected boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX authorization_role_name ON authorization_roles(lower(btrim(name)));
CREATE UNIQUE INDEX authorization_protected_role ON authorization_roles(protected) WHERE protected;
INSERT INTO authorization_roles(name,protected) VALUES ('Administrator',true),('Dispatcher',false),('Technician',false),('No permissions',false);

CREATE TABLE authorization_permissions (
    permission text NOT NULL,
    scope text NOT NULL CHECK(scope IN ('A','J','C','O','U','S')),
    prerequisite text,
    PRIMARY KEY(permission,scope)
);
INSERT INTO authorization_permissions VALUES
 ('customer.view','A',NULL),('customer.view','C',NULL),
 ('customer.create','A','customer.view'),('customer.edit','A','customer.view'),
 ('customer.financial.view','A','customer.view'),
 ('customer.archive','A','customer.view'),('customer.archived.view','A','customer.view'),('customer.restore','A','customer.view'),
 ('customer.export','A','customer.view'),('customer.financial.export','A','customer.financial.view'),
 ('directory.view','A',NULL),('administration','S',NULL);
-- Future domains have explicit catalog entries but retain legacy enforcement
-- until their owning slice. Their catalog is not an alternate authority path.
INSERT INTO authorization_permissions
 SELECT domain||'.view',scope,NULL FROM unnest(ARRAY['contact','location','asset','project']) domain CROSS JOIN unnest(ARRAY['A','C']) scope;
INSERT INTO authorization_permissions
 SELECT domain||'.'||action,'A',domain||'.view' FROM unnest(ARRAY['contact','location','asset','project']) domain CROSS JOIN unnest(ARRAY['create','edit','archive','archived.view','restore','export']) action;
INSERT INTO authorization_permissions
 SELECT 'job.'||action,scope,CASE WHEN action='view' THEN NULL ELSE 'job.view' END FROM unnest(ARRAY['view','status','subtask.complete','subtask.reopen','field_notes.edit','fields.edit']) action CROSS JOIN unnest(ARRAY['A','J']) scope;
INSERT INTO authorization_permissions
 SELECT 'job.'||action,'A','job.view' FROM unnest(ARRAY['create','edit','relations.edit','schedule','assign','subtask.manage','archive','archived.view','restore']) action;
INSERT INTO authorization_permissions
 SELECT domain||'.'||action,scope,CASE WHEN action='view' THEN NULL ELSE domain||'.view' END FROM unnest(ARRAY['estimate','invoice']) domain CROSS JOIN unnest(ARRAY['view','create','edit','status','provenance.view','pdf.generate','pdf.download','delivery.request']) action CROSS JOIN unnest(ARRAY['A','J']) scope;
INSERT INTO authorization_permissions
 SELECT domain||'.'||action,'A',domain||'.view' FROM unnest(ARRAY['estimate','invoice','item','lease']) domain CROSS JOIN unnest(ARRAY['archive','archived.view','restore','export']) action;
INSERT INTO authorization_permissions VALUES
 ('estimate.convert','A','estimate.view'),('estimate.convert','J','estimate.view'),
 ('invoice.finalize','A','invoice.view'),('invoice.finalize','J','invoice.view'),
 ('invoice.revert','A','invoice.view'),('invoice.revert','J','invoice.view'),
 ('item.view','A',NULL),('pricebook.view','A','item.view'),('cost.view','A','item.view'),
 ('item.create','A','item.view'),('item.edit','A','item.view'),('pricebook.create','A','pricebook.view'),('pricebook.edit','A','pricebook.view'),('cost.edit','A','cost.view'),('cost.export','A','cost.view'),('pricebook.export','A','pricebook.view'),
 ('payment.record','A','invoice.view'),('payment.record','J','invoice.view'),
 ('payment.credit','A','customer.financial.view'),('credit.apply','A','customer.financial.view'),('credit.refund','A','customer.financial.view'),('payment.reverse','A','customer.financial.view'),('credit.application.reverse','A','customer.financial.view'),('credit.refund.reverse','A','customer.financial.view'),
 ('credit.view','A','customer.financial.view'),('settlement.view','A','customer.financial.view'),('credit.export','A','credit.view'),('settlement.export','A','settlement.view'),
 ('pricing.view','A','job.view'),('pricing.view','J','job.view'),
 ('lease.view','A',NULL),('lease.manage','A','lease.view'),('lease.billing.view','A','lease.view'),('lease.billing.manage','A','lease.billing.view'),
 ('project.status','A','project.view'),('asset.transfer','A','asset.view'),('asset.place','A','asset.view'),('asset.return','A','asset.view'),('asset.history.correct','A','asset.view');
INSERT INTO authorization_permissions
 SELECT 'time.'||action,scope,CASE WHEN action='view' THEN NULL ELSE 'time.view' END FROM unnest(ARRAY['view','create','correct','void','export','gps.view','gps.export']) action CROSS JOIN unnest(ARRAY['A','J','O']) scope;
INSERT INTO authorization_permissions VALUES ('time.clock_in','O','time.view'),('time.clock_out','O','time.view'),('time.gps.capture','O','time.view');
INSERT INTO authorization_permissions
 SELECT 'service_entry.'||action,scope,CASE WHEN action='view' THEN NULL ELSE 'service_entry.view' END FROM unnest(ARRAY['view','create','revise']) action CROSS JOIN unnest(ARRAY['A','J']) scope;
INSERT INTO authorization_permissions VALUES ('service_entry.export','A','service_entry.view');
INSERT INTO authorization_permissions
 SELECT action,scope,NULL FROM unnest(ARRAY['comment.add','file.download','file.upload','tag.attach','tag.detach','job.export','activity.export']) action CROSS JOIN unnest(ARRAY['A','J']) scope;
INSERT INTO authorization_permissions
 SELECT action,scope,NULL FROM unnest(ARRAY['comment.remove','file.rename','file.remove']) action CROSS JOIN unnest(ARRAY['A','J','O']) scope;
INSERT INTO authorization_permissions
 SELECT 'configuration.'||domain||'.'||action,'U',CASE WHEN action='view' THEN NULL ELSE 'configuration.'||domain||'.view' END FROM unnest(ARRAY['company','documents','mail','maps','workflow','assets','tags','fields']) domain CROSS JOIN unnest(ARRAY['view','manage']) action;
INSERT INTO authorization_permissions VALUES ('configuration.mail.test','U','configuration.mail.view');
INSERT INTO authorization_permissions SELECT permission,'S',NULL FROM unnest(ARRAY[
 'user.access.view','user.create','user.invite','user.identity.edit','user.role.assign','user.enabled.edit','user.welcome.resend','user.recovery.resend','user.password.require_change','user.sessions.revoke',
 'role.create','role.edit','role.rename','role.remove','audit.view','audit.export','dashboard.defaults','backup.create','backup.restore','email.enable',
 'security.password.view','security.password.manage','security.session.view','security.session.manage','security.offline.view','security.offline.manage','security.open_tracking.view','security.open_tracking.manage']) permission;
UPDATE authorization_permissions SET prerequisite='job.view' WHERE scope='C';

CREATE TABLE authorization_grants (
    role_id bigint NOT NULL REFERENCES authorization_roles(id),
    permission text NOT NULL,
    scope text NOT NULL,
    PRIMARY KEY(role_id,permission,scope),
    FOREIGN KEY(permission,scope) REFERENCES authorization_permissions(permission,scope)
);
CREATE UNIQUE INDEX authorization_protected_grant_action ON authorization_grants(role_id,permission) WHERE role_id=1;
-- Broadest valid grant only; own-only actions remain O.
INSERT INTO authorization_grants
 SELECT 1,permission,(array_agg(scope ORDER BY CASE scope WHEN 'A' THEN 0 WHEN 'J' THEN 1 WHEN 'C' THEN 2 WHEN 'O' THEN 3 ELSE 4 END))[1]
 FROM authorization_permissions GROUP BY permission;
-- Defaults are for fresh installations only; never an upgrade mapping.
INSERT INTO authorization_grants SELECT 2,permission,scope FROM authorization_permissions
 WHERE scope='A' AND (SELECT fresh_install FROM authorization_state);
INSERT INTO authorization_grants SELECT 2,permission,scope FROM authorization_permissions
 WHERE permission IN ('time.clock_in','time.clock_out','time.gps.capture') AND (SELECT fresh_install FROM authorization_state);
INSERT INTO authorization_grants SELECT 3,permission,scope FROM authorization_permissions
 WHERE (SELECT fresh_install FROM authorization_state) AND (
 (scope='C' AND permission IN ('customer.view','contact.view','location.view','asset.view','project.view')) OR
 (scope='J' AND (permission LIKE 'job.%' AND permission NOT IN ('job.export') OR permission LIKE 'estimate.%' OR permission LIKE 'invoice.%' OR permission LIKE 'service_entry.%' OR permission IN ('payment.record','pricing.view','comment.add','file.download','file.upload'))) OR
 (scope='O' AND (permission LIKE 'time.%' AND permission NOT IN ('time.export','time.gps.export') OR permission IN ('comment.remove','file.rename','file.remove'))) OR
 (scope='A' AND permission IN ('item.view','pricebook.view')));

ALTER TABLE users ADD COLUMN authorization_role_id bigint REFERENCES authorization_roles(id);
ALTER TABLE customers ADD COLUMN service_notes text NOT NULL DEFAULT '';
CREATE TABLE authorization_reviews (
    user_id bigint PRIMARY KEY REFERENCES users(id),
    role_id bigint NOT NULL REFERENCES authorization_roles(id),
    user_revision timestamptz NOT NULL,
    role_revision bigint NOT NULL,
    approved_by bigint NOT NULL REFERENCES users(id),
    approved_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE authorization_audit (
    id bigserial PRIMARY KEY,
    actor_id bigint NOT NULL,
    role_id bigint,
    permission text NOT NULL,
    scope text NOT NULL,
    target_type text NOT NULL,
    target_id bigint NOT NULL,
    channel text NOT NULL,
    outcome text NOT NULL DEFAULT 'success',
    recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    detail jsonb NOT NULL DEFAULT '{}'
);
CREATE FUNCTION authorization_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'immutable authorization history'; END $$;
CREATE TRIGGER authorization_audit_immutable BEFORE UPDATE OR DELETE ON authorization_audit FOR EACH ROW EXECUTE FUNCTION authorization_immutable();
CREATE FUNCTION authorization_role_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.protected THEN RAISE EXCEPTION 'protected Administrator is immutable'; END IF;
 IF TG_OP='UPDATE' AND NEW.protected THEN RAISE EXCEPTION 'protected identity cannot be reassigned'; END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER authorization_role_guard BEFORE UPDATE OR DELETE ON authorization_roles FOR EACH ROW EXECUTE FUNCTION authorization_role_guard();
CREATE FUNCTION authorization_grant_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM authorization_roles WHERE id=OLD.role_id AND protected) AND NOT (TG_OP='UPDATE' AND pg_trigger_depth()>1) THEN RAISE EXCEPTION 'protected grants are immutable'; END IF;
 IF TG_OP='UPDATE' THEN RETURN NEW; END IF;
 RETURN OLD;
END $$;
CREATE TRIGGER authorization_grant_guard BEFORE UPDATE OR DELETE ON authorization_grants FOR EACH ROW EXECUTE FUNCTION authorization_grant_guard();
CREATE FUNCTION authorization_new_permission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO authorization_grants(role_id,permission,scope) VALUES(1,NEW.permission,NEW.scope)
 ON CONFLICT(role_id,permission) WHERE role_id=1 DO UPDATE SET scope=excluded.scope
 WHERE CASE excluded.scope WHEN 'A' THEN 0 WHEN 'J' THEN 1 WHEN 'C' THEN 2 WHEN 'O' THEN 3 ELSE 4 END
 < CASE authorization_grants.scope WHEN 'A' THEN 0 WHEN 'J' THEN 1 WHEN 'C' THEN 2 WHEN 'O' THEN 3 ELSE 4 END;
 RETURN NEW;
END $$;
CREATE TRIGGER authorization_new_permission AFTER INSERT ON authorization_permissions FOR EACH ROW EXECUTE FUNCTION authorization_new_permission();

-- A single DB transaction mutex also covers legacy writers. The deferred
-- invariant sees the final state, permitting atomic replacement but no gap.
CREATE FUNCTION authorization_lock() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_advisory_xact_lock(190019); RETURN NULL; END $$;
CREATE TRIGGER authorization_users_lock BEFORE INSERT OR UPDATE OR DELETE ON users FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();
CREATE TRIGGER authorization_roles_lock BEFORE INSERT OR UPDATE OR DELETE ON authorization_roles FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();
CREATE TRIGGER authorization_grants_lock BEFORE INSERT OR UPDATE OR DELETE ON authorization_grants FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();
CREATE TRIGGER authorization_sessions_lock BEFORE INSERT OR UPDATE OR DELETE ON sessions FOR EACH STATEMENT EXECUTE FUNCTION authorization_lock();
CREATE FUNCTION authorization_user_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.role IS DISTINCT FROM OLD.role THEN RAISE EXCEPTION 'legacy Role assignment is sealed; use explicit Role identity'; END IF;
 IF TG_OP='INSERT' THEN UPDATE authorization_state SET bootstrapped=true; END IF;
 IF (SELECT ready FROM authorization_state) THEN
   IF NEW.authorization_role_id IS NULL THEN
     IF TG_OP='INSERT' AND (SELECT fresh_install FROM authorization_state) AND NOT EXISTS(SELECT 1 FROM users) AND NEW.role='admin' THEN NEW.authorization_role_id:=1;
     ELSE
       SELECT min(r.id) INTO NEW.authorization_role_id FROM authorization_roles r
       WHERE NOT r.protected AND NOT EXISTS(SELECT 1 FROM authorization_grants g WHERE g.role_id=r.id);
       IF NEW.authorization_role_id IS NULL THEN RAISE EXCEPTION 'create an empty-grant Role before creating a new user'; END IF;
     END IF;
   END IF;
 END IF;
 IF TG_OP='UPDATE' THEN NEW.updated_at:=clock_timestamp(); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER authorization_user_guard BEFORE INSERT OR UPDATE ON users FOR EACH ROW EXECUTE FUNCTION authorization_user_guard();
CREATE FUNCTION authorization_last_admin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (SELECT ready AND bootstrapped FROM authorization_state) AND NOT EXISTS(
   SELECT 1 FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id
   WHERE r.protected AND u.is_active AND u.company_id IS NOT NULL AND u.password_hash ~ '^\$2[aby]\$(0[4-9]|[12][0-9]|3[01])\$[./A-Za-z0-9]{53}$') THEN
   RAISE EXCEPTION 'at least one enabled authenticatable protected Administrator must remain';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER authorization_last_admin AFTER INSERT OR UPDATE OR DELETE ON users DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION authorization_last_admin();
