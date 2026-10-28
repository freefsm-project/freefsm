-- Archived visibility is a lifecycle prerequisite for either independent facet;
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id WHERE l.lifecycle='draft') THEN
  RAISE EXCEPTION 'Native Draft coverage requires explicit mapping review; recorded rows are retained';
 END IF;
END $$;
ALTER TABLE leases ADD COLUMN lifecycle_revision bigint NOT NULL DEFAULT 0;

CREATE FUNCTION native_draft_coverage_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
 IF TG_TABLE_NAME='leases' THEN target:=NEW.id; ELSE target:=NEW.lease_id; END IF;
 IF EXISTS(SELECT 1 FROM leases l JOIN lease_asset_dependencies d ON d.lease_id=l.id WHERE l.id=target AND l.lifecycle='draft') THEN
  RAISE EXCEPTION 'Native Draft intended Assets are not dated coverage';
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER native_draft_coverage AFTER INSERT OR UPDATE ON lease_asset_dependencies DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION native_draft_coverage_guard();
CREATE CONSTRAINT TRIGGER native_draft_lifecycle_coverage AFTER INSERT OR UPDATE ON leases DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION native_draft_coverage_guard();

-- Opaque legacy collaboration/audit references are not agreement facts. Never
-- let a newly allocated identity silently adopt them, even without a projection.
CREATE FUNCTION lease_retained_identity_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM comments WHERE object_type='lease' AND object_id=NEW.id)
  OR EXISTS(SELECT 1 FROM files WHERE object_type='lease' AND object_id=NEW.id)
  OR EXISTS(SELECT 1 FROM tag_links WHERE object_type='lease' AND object_id=NEW.id)
  OR EXISTS(SELECT 1 FROM authorization_audit WHERE target_type='lease' AND target_id=NEW.id) THEN
  RAISE EXCEPTION 'Existing Lease references require explicit mapping review; identity cannot be adopted';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_retained_identity BEFORE INSERT ON leases FOR EACH ROW EXECUTE FUNCTION lease_retained_identity_guard();
-- it does not grant base View, Billing View, Archive, Restore or Manage.
UPDATE authorization_permissions SET prerequisite=NULL WHERE permission='lease.archived.view' AND scope='A';
INSERT INTO authorization_view_requirements VALUES
 ('lease.archived.view','A','lease-facet','lease.view','A'),
 ('lease.archived.view','A','lease-facet','lease.billing.view','A'),
 ('lease.restore','A','base','lease.view','A');

-- A Draft may retain a historical premises selection after state-only restore.
-- New selection is checked separately against OLD, while finalized coverage
-- continues to require active premises. No dependency or agreement is backfilled.
CREATE OR REPLACE FUNCTION lease_dependency_validate(lease bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE l lease_master_dependencies;
BEGIN
 SELECT * INTO l FROM lease_master_dependencies WHERE lease_id=lease;
 IF NOT FOUND THEN RETURN; END IF;
 IF NOT EXISTS(SELECT 1 FROM customers WHERE id=l.customer_id AND company_id=l.company_id) OR
  (l.premises_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM locations WHERE id=l.premises_id AND company_id=l.company_id AND object_type='customer' AND object_id=l.customer_id)) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease dependencies must belong to the same Customer';
 END IF;
 IF NOT l.archived AND l.lifecycle <> 'terminated' THEN
  IF NOT EXISTS(SELECT 1 FROM customers WHERE id=l.customer_id AND deleted_at IS NULL) OR
   (l.lifecycle<>'draft' AND l.premises_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM locations WHERE id=l.premises_id AND deleted_at IS NULL)) THEN
   RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease registration/finalization requires active Customer and premises';
  END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id
  WHERE d.lease_id=lease AND (a.company_id IS DISTINCT FROM l.company_id OR a.ownership_class <> 'company_owned')) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage requires Company-owned Assets';
 END IF;
 IF NOT l.archived AND l.lifecycle IN ('scheduled','active','month_to_month') AND EXISTS(
  SELECT 1 FROM lease_asset_dependencies d JOIN assets a ON a.id=d.asset_id
  WHERE d.lease_id=lease AND (d.ends_on IS NULL OR d.ends_on >= lease_dependency_today(l.company_id))
   AND (a.deleted_at IS NOT NULL OR (a.customer_id IS NOT NULL AND a.customer_id<>l.customer_id))) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Lease coverage requires active Assets without another Customer placement';
 END IF;
END $$;

CREATE FUNCTION lease_new_premises_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.lifecycle='draft' AND NEW.premises_id IS NOT NULL AND
  -- The replacement adapter uses INSERT ON CONFLICT before UPDATE. Compare
  -- the persisted identity, not TG_OP, so its retained upsert is not a selection.
  NOT EXISTS(SELECT 1 FROM lease_master_dependencies prior WHERE prior.lease_id=NEW.lease_id
   AND prior.company_id=NEW.company_id AND prior.customer_id=NEW.customer_id AND prior.premises_id=NEW.premises_id) AND
  NOT EXISTS(SELECT 1 FROM locations WHERE id=NEW.premises_id AND company_id=NEW.company_id
   AND object_type='customer' AND object_id=NEW.customer_id AND deleted_at IS NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='New Draft premises selection requires an active same-Customer Location';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_new_premises_guard BEFORE INSERT OR UPDATE ON lease_master_dependencies FOR EACH ROW EXECUTE FUNCTION lease_new_premises_guard();

CREATE FUNCTION lease_archive_readonly_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.deleted_at IS NOT NULL AND NEW.deleted_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'Archived Lease history is read-only; restore before editing';
 END IF;
 IF OLD.deleted_at IS DISTINCT FROM NEW.deleted_at THEN
  IF (to_jsonb(OLD)-ARRAY['deleted_at','updated_at','details_revision','lifecycle_revision']) IS DISTINCT FROM
     (to_jsonb(NEW)-ARRAY['deleted_at','updated_at','details_revision','lifecycle_revision']) THEN
   RAISE EXCEPTION 'Lease archive and restore change state only';
  END IF;
  IF NEW.lifecycle_revision<>OLD.lifecycle_revision+1 OR NEW.details_revision<>OLD.details_revision+1 THEN
   RAISE EXCEPTION 'Lease state transitions must advance their revision guards';
  END IF;
 ELSIF NEW.lifecycle_revision<>OLD.lifecycle_revision THEN
  RAISE EXCEPTION 'Lease lifecycle revision changes only with archive or restore';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER lease_archive_readonly BEFORE UPDATE ON leases FOR EACH ROW EXECUTE FUNCTION lease_archive_readonly_guard();

CREATE FUNCTION lease_child_readonly_guard() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint; old_target bigint;
BEGIN
 -- Protected complete-instance restore may relocate physical bytes to the
 -- destination upload root. This narrow path-only effect is not File editing.
 IF TG_TABLE_NAME='files' AND TG_OP='UPDATE' AND current_setting('freefsm.backup_path_rebase',true)='allowed'
  AND (to_jsonb(OLD)-'file_path')=(to_jsonb(NEW)-'file_path') THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME IN ('lease_intended_assets','lease_draft_billing','lease_draft_asset_terms') THEN
  IF TG_OP<>'DELETE' THEN target:=NEW.lease_id; END IF;
  IF TG_OP<>'INSERT' THEN old_target:=OLD.lease_id; END IF;
 ELSE
  IF TG_OP<>'DELETE' AND NEW.object_type='lease' THEN target:=NEW.object_id; END IF;
  IF TG_OP<>'INSERT' AND OLD.object_type='lease' THEN old_target:=OLD.object_id; END IF;
 END IF;
 IF EXISTS(SELECT 1 FROM leases WHERE id IN (target,old_target) AND deleted_at IS NOT NULL) THEN
  RAISE EXCEPTION USING ERRCODE='23514', MESSAGE='Archived Lease collaboration and terms are read-only';
 END IF;
 RETURN CASE WHEN TG_OP='DELETE' THEN OLD ELSE NEW END;
END $$;
CREATE TRIGGER lease_intended_readonly BEFORE INSERT OR UPDATE OR DELETE ON lease_intended_assets FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_billing_readonly BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_billing FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_terms_readonly BEFORE INSERT OR UPDATE OR DELETE ON lease_draft_asset_terms FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_comments_readonly BEFORE INSERT OR UPDATE OR DELETE ON comments FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_files_readonly BEFORE INSERT OR UPDATE OR DELETE ON files FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_tags_readonly BEFORE INSERT OR UPDATE OR DELETE ON tag_links FOR EACH ROW EXECUTE FUNCTION lease_child_readonly_guard();
CREATE TRIGGER lease_comments_fence BEFORE INSERT OR UPDATE OR DELETE ON comments FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_files_fence BEFORE INSERT OR UPDATE OR DELETE ON files FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
CREATE TRIGGER lease_tags_fence BEFORE INSERT OR UPDATE OR DELETE ON tag_links FOR EACH STATEMENT EXECUTE FUNCTION master_write_lock();
