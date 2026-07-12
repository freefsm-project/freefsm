-- Category semantics replace behavior inferred from editable status labels.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM statuses
    GROUP BY workflow_id, lower(btrim(name))
    HAVING count(*) > 1
  ) THEN
    RAISE EXCEPTION 'status category preflight: duplicate case-insensitive labels exist within a workflow';
  END IF;
END $$;

ALTER TABLE statuses ADD COLUMN category_key TEXT;
ALTER TABLE statuses ADD COLUMN category_order INT;
ALTER TABLE statuses ADD COLUMN is_category_default BOOLEAN NOT NULL DEFAULT false;

-- Known labels retain their rows. Unknown labels intentionally enter the fallback category.
UPDATE statuses s SET category_key = CASE w.object_type
  WHEN 'job' THEN 'job:' || CASE
    WHEN lower(btrim(s.name))='new' THEN 'new'
    WHEN lower(btrim(s.name))='travel time' THEN 'travel_time'
    WHEN lower(btrim(s.name))='in progress' THEN 'in_progress'
    WHEN lower(btrim(s.name)) IN ('completed') THEN 'completed'
    WHEN lower(btrim(s.name)) IN ('canceled','cancelled') THEN 'canceled'
    ELSE 'pending' END
  WHEN 'project' THEN 'project:' || CASE
    WHEN lower(btrim(s.name)) IN ('opportunity','accepted','new') THEN 'new'
    WHEN lower(btrim(s.name)) IN ('planning','in progress') THEN 'in_progress'
    WHEN lower(btrim(s.name))='completed' THEN 'completed'
    WHEN lower(btrim(s.name)) IN ('canceled','cancelled') THEN 'canceled'
    ELSE 'pending' END
  WHEN 'estimate' THEN 'estimate:' || CASE
    WHEN lower(btrim(s.name))='draft' THEN 'draft'
    WHEN lower(btrim(s.name))='sent' THEN 'sent'
    WHEN lower(btrim(s.name)) IN ('accepted','approved') THEN 'accepted'
    WHEN lower(btrim(s.name)) IN ('rejected','declined') THEN 'rejected'
    WHEN lower(btrim(s.name))='completed' THEN 'completed'
    ELSE 'estimate' END
  WHEN 'invoice' THEN 'invoice:' || CASE
    WHEN lower(btrim(s.name))='draft' THEN 'draft'
    WHEN lower(btrim(s.name))='sent' THEN 'sent'
    WHEN lower(btrim(s.name))='void' THEN 'void'
    WHEN lower(btrim(s.name))='partially paid' THEN 'partially_paid'
    WHEN lower(btrim(s.name))='paid' THEN 'paid'
    ELSE 'invoiced' END
  END
FROM status_workflows w WHERE w.id=s.workflow_id;

CREATE TEMP TABLE status_category_seed(object_type text, key text, label text, color text, ordinal int, creation_default boolean) ON COMMIT DROP;
INSERT INTO status_category_seed VALUES
 ('job','job:new','New','#3B82F6',1,true),('job','job:travel_time','Travel Time','#8B5CF6',2,false),
 ('job','job:in_progress','In Progress','#F59E0B',3,false),('job','job:pending','Pending','#6B7280',4,false),
 ('job','job:completed','Completed','#10B981',5,false),('job','job:canceled','Canceled','#EF4444',6,false),
 ('project','project:new','Opportunity','#3B82F6',1,true),('project','project:in_progress','In Progress','#F59E0B',2,false),
 ('project','project:pending','Pending','#8B5CF6',3,false),('project','project:completed','Completed','#10B981',4,false),
 ('project','project:canceled','Canceled','#EF4444',5,false),
 ('estimate','estimate:draft','Draft','#6B7280',1,true),('estimate','estimate:estimate','Estimate','#8B5CF6',2,false),
 ('estimate','estimate:sent','Sent','#3B82F6',3,false),('estimate','estimate:accepted','Accepted','#10B981',4,false),
 ('estimate','estimate:rejected','Rejected','#EF4444',5,false),('estimate','estimate:completed','Completed','#059669',6,false),
 ('invoice','invoice:draft','Draft','#6B7280',1,true),('invoice','invoice:invoiced','Invoiced','#8B5CF6',2,false),
 ('invoice','invoice:sent','Sent','#3B82F6',3,false),('invoice','invoice:partially_paid','Partially Paid','#F59E0B',4,false),
 ('invoice','invoice:paid','Paid','#10B981',5,false),('invoice','invoice:void','Void','#EF4444',6,false);

INSERT INTO statuses(company_id,workflow_id,name,color,sort_order,category_key,category_order,is_category_default)
SELECT w.company_id,w.id,c.label,c.color,c.ordinal,c.key,1,true
FROM status_workflows w JOIN status_category_seed c ON c.object_type=w.object_type
WHERE NOT EXISTS(SELECT 1 FROM statuses s WHERE s.workflow_id=w.id AND s.category_key=c.key);

-- Invoice payment labels are projections, not manual state. Fixed slots must exist
-- before references are reassigned because historical workflows may have no Sent row.
UPDATE invoices i SET status_id=sent.id
FROM statuses old
JOIN status_workflows w ON w.id=old.workflow_id AND w.object_type='invoice'
JOIN LATERAL (
  SELECT s2.id FROM statuses s2
  WHERE s2.workflow_id=old.workflow_id AND s2.category_key='invoice:sent'
  ORDER BY (lower(btrim(s2.name))='sent') DESC,s2.id LIMIT 1
) sent ON true
WHERE i.status_id=old.id AND old.category_key IN ('invoice:paid','invoice:partially_paid');

WITH ranked AS (
 SELECT s.id,row_number() OVER(PARTITION BY s.workflow_id,s.category_key ORDER BY
   CASE
    WHEN s.category_key='project:new' AND lower(btrim(s.name))='opportunity' THEN 0
    WHEN lower(btrim(s.name))=lower(replace(split_part(s.category_key,':',2),'_',' ')) THEN 0
    ELSE 1 END,s.sort_order,s.id) rn
 FROM statuses s
)
UPDATE statuses s SET is_category_default=(r.rn=1),category_order=r.rn
FROM ranked r WHERE r.id=s.id;

-- Collapse Invoice to its fixed six slots, retaining the preferred known row in each category.
CREATE TEMP TABLE invoice_status_replacements(old_id bigint PRIMARY KEY,new_id bigint NOT NULL) ON COMMIT DROP;
INSERT INTO invoice_status_replacements
SELECT s.id, keep.id FROM statuses s
JOIN status_workflows w ON w.id=s.workflow_id AND w.object_type='invoice'
JOIN LATERAL (SELECT x.id FROM statuses x WHERE x.workflow_id=s.workflow_id AND x.category_key=s.category_key
 ORDER BY x.is_category_default DESC,x.id LIMIT 1) keep ON true
WHERE s.id<>keep.id;
UPDATE invoices i SET status_id=r.new_id FROM invoice_status_replacements r WHERE i.status_id=r.old_id;
DELETE FROM statuses s USING invoice_status_replacements r WHERE s.id=r.old_id;

ALTER TABLE statuses ALTER COLUMN category_key SET NOT NULL;
ALTER TABLE statuses ALTER COLUMN category_order SET NOT NULL;
ALTER TABLE statuses ADD CONSTRAINT statuses_id_company_unique UNIQUE(id,company_id);
ALTER TABLE statuses DROP COLUMN estimate_convertible;
DROP INDEX IF EXISTS statuses_one_document_role_per_workflow;
DROP TRIGGER IF EXISTS status_conversion_capabilities ON statuses;
DROP FUNCTION IF EXISTS validate_status_conversion_capabilities();
ALTER TABLE statuses DROP COLUMN document_role;

ALTER TABLE statuses ADD CONSTRAINT statuses_category_order_positive CHECK(category_order>0);
ALTER TABLE statuses ADD CONSTRAINT statuses_name_not_blank CHECK(btrim(name)<>'');
ALTER TABLE statuses ADD CONSTRAINT statuses_category_key_valid CHECK(category_key IN (
 'job:new','job:travel_time','job:in_progress','job:pending','job:completed','job:canceled',
 'project:new','project:in_progress','project:pending','project:completed','project:canceled',
 'estimate:draft','estimate:estimate','estimate:sent','estimate:accepted','estimate:rejected','estimate:completed',
 'invoice:draft','invoice:invoiced','invoice:sent','invoice:partially_paid','invoice:paid','invoice:void'));
CREATE UNIQUE INDEX statuses_name_workflow_ci_unique ON statuses(workflow_id,lower(btrim(name)));
CREATE UNIQUE INDEX statuses_category_order_unique ON statuses(workflow_id,category_key,category_order);
CREATE UNIQUE INDEX statuses_category_default_unique ON statuses(workflow_id,category_key) WHERE is_category_default;

CREATE FUNCTION validate_status_category() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE typ text;
BEGIN
 SELECT object_type INTO typ FROM status_workflows WHERE id=NEW.workflow_id;
 IF split_part(NEW.category_key,':',1) IS DISTINCT FROM typ THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='status category does not belong to workflow type';
 END IF;
 IF typ='invoice' AND NEW.category_key IN ('invoice:partially_paid','invoice:paid') AND TG_OP='UPDATE' AND
    NEW.category_key IS DISTINCT FROM OLD.category_key THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='payment-derived invoice categories cannot be manually assigned';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER status_category_guard BEFORE INSERT OR UPDATE OF workflow_id,category_key ON statuses
 FOR EACH ROW EXECUTE FUNCTION validate_status_category();

CREATE FUNCTION check_status_category_invariant(wid bigint,key text) RETURNS void LANGUAGE plpgsql AS $$
DECLARE typ text;
BEGIN
 SELECT object_type INTO typ FROM status_workflows WHERE id=wid;
 IF NOT EXISTS(SELECT 1 FROM statuses WHERE workflow_id=wid AND category_key=key) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='category must retain at least one status';
 END IF;
 IF (SELECT count(*) FROM statuses WHERE workflow_id=wid AND category_key=key AND is_category_default)<>1 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='category must have exactly one default status';
 END IF;
 IF (SELECT count(DISTINCT category_key) FROM statuses WHERE workflow_id=wid) <>
    (CASE typ WHEN 'project' THEN 5 ELSE 6 END) THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='workflow must contain every fixed category';
 END IF;
 IF typ='invoice' AND (SELECT count(*) FROM statuses WHERE workflow_id=wid)<>6 THEN
   RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='invoice workflow must retain exactly six category slots';
 END IF;
END $$;
CREATE FUNCTION enforce_status_category_invariants() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP<>'INSERT' THEN PERFORM check_status_category_invariant(OLD.workflow_id,OLD.category_key); END IF;
 IF TG_OP<>'DELETE' AND (TG_OP='INSERT' OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id OR NEW.category_key IS DISTINCT FROM OLD.category_key) THEN
   PERFORM check_status_category_invariant(NEW.workflow_id,NEW.category_key);
 END IF;
 RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER status_category_invariants AFTER INSERT OR UPDATE OR DELETE ON statuses
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_status_category_invariants();

CREATE OR REPLACE FUNCTION guard_settled_invoice_update() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE status_category text;
BEGIN
 IF invoice_has_active_settlement(OLD.id) AND
    (NEW.customer_id IS DISTINCT FROM OLD.customer_id OR NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate) THEN
   RAISE EXCEPTION 'settled invoice monetary fields and customer are locked';
 END IF;
 IF NEW.status_id IS DISTINCT FROM OLD.status_id THEN
   SELECT category_key INTO status_category FROM statuses WHERE id=NEW.status_id;
   IF status_category IN ('invoice:paid','invoice:partially_paid') THEN RAISE EXCEPTION 'payment categories are ledger-derived'; END IF;
   IF invoice_has_active_settlement(OLD.id) THEN RAISE EXCEPTION 'manual invoice status requires zero active settlement'; END IF;
 END IF;
 IF NEW.deleted_at IS NOT NULL AND OLD.deleted_at IS NULL THEN
   SELECT category_key INTO status_category FROM statuses WHERE id=NEW.status_id;
   IF coalesce(status_category,'')<>'invoice:void' AND NEW.settlement_state<>'paid' THEN RAISE EXCEPTION 'invoice archive requires paid or void'; END IF;
 END IF;
 IF NEW.settlement_state IS DISTINCT FROM OLD.settlement_state AND NOT (
      NOT invoice_has_active_settlement(OLD.id) AND
      (NEW.line_items IS DISTINCT FROM OLD.line_items OR NEW.tax_rate IS DISTINCT FROM OLD.tax_rate)) AND
    current_setting('freefsm.settlement_projection',true) IS DISTINCT FROM 'on' THEN RAISE EXCEPTION 'settlement_state is ledger-derived'; END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER job_status_ownership BEFORE INSERT OR UPDATE OF company_id,status_id ON jobs
 FOR EACH ROW EXECUTE FUNCTION validate_document_status_ownership('job');
CREATE TRIGGER project_status_ownership BEFORE INSERT OR UPDATE OF company_id,status_id ON projects
 FOR EACH ROW EXECUTE FUNCTION validate_document_status_ownership('project');

CREATE FUNCTION guard_record_status_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status_id IS DISTINCT FROM OLD.status_id AND
    current_setting('freefsm.status_transition',true) IS DISTINCT FROM 'allowed' THEN
   RAISE EXCEPTION USING ERRCODE='42501',MESSAGE='status update requires statusflow';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER job_status_update_guard BEFORE UPDATE OF status_id ON jobs FOR EACH ROW EXECUTE FUNCTION guard_record_status_update();
CREATE TRIGGER project_status_update_guard BEFORE UPDATE OF status_id ON projects FOR EACH ROW EXECUTE FUNCTION guard_record_status_update();
CREATE TRIGGER estimate_status_update_guard BEFORE UPDATE OF status_id ON estimates FOR EACH ROW EXECUTE FUNCTION guard_record_status_update();
CREATE TRIGGER invoice_status_update_guard BEFORE UPDATE OF status_id ON invoices FOR EACH ROW EXECUTE FUNCTION guard_record_status_update();

CREATE VIEW status_category_read AS
SELECT s.*,w.object_type,split_part(s.category_key,':',2) category,w.company_id workflow_company_id
FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id;

CREATE VIEW invoice_effective_status AS
SELECT i.id invoice_id,i.company_id,
 CASE
  WHEN manual.category_key='invoice:void' THEN voids.id
  WHEN manual.category_key='invoice:draft' AND NOT invoice_has_active_settlement(i.id) THEN drafts.id
  WHEN i.settlement_state='partially_paid' AND invoice_has_active_settlement(i.id) THEN partial.id
  WHEN (i.settlement_state='paid' AND invoice_has_active_settlement(i.id)) OR
       (manual.category_key<>'invoice:draft' AND i.settlement_state='paid') THEN paid.id
  ELSE manual.id END status_id
FROM invoices i JOIN statuses manual ON manual.id=i.status_id
JOIN statuses drafts ON drafts.workflow_id=manual.workflow_id AND drafts.category_key='invoice:draft'
JOIN statuses partial ON partial.workflow_id=manual.workflow_id AND partial.category_key='invoice:partially_paid'
JOIN statuses paid ON paid.workflow_id=manual.workflow_id AND paid.category_key='invoice:paid'
JOIN statuses voids ON voids.workflow_id=manual.workflow_id AND voids.category_key='invoice:void';

COMMENT ON TABLE status_workflows IS 'Compatibility tenant container owned by internal/statusflow; remove after all consumers resolve workflow by company and object type through that module.';
