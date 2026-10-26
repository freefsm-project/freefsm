-- Historical rows have no recorded dispatch boundary. Do not backfill one from
-- state, attempt count, or current document visibility.
ALTER TABLE document_deliveries ADD COLUMN handoff_started_at TIMESTAMPTZ;
ALTER TABLE document_deliveries ADD COLUMN dispatch_boundary_known BOOLEAN;
-- Only future requests get this default. Installed in-flight attempts remain
-- unknown until a fresh attempt is actually claimed by the new dispatcher.
ALTER TABLE document_deliveries ALTER COLUMN dispatch_boundary_known SET DEFAULT true;
ALTER TABLE document_deliveries ADD COLUMN cancelled_at TIMESTAMPTZ;
ALTER TABLE document_deliveries DROP CONSTRAINT document_deliveries_state_check;
ALTER TABLE document_deliveries ADD CONSTRAINT document_deliveries_state_check
 CHECK(state IN ('queued','sending','accepted','delivered','bounced','failed','cancelled'));
ALTER TABLE document_deliveries ADD CONSTRAINT cancelled_delivery_has_reason
 CHECK(state<>'cancelled' OR (cancelled_at IS NOT NULL AND btrim(last_error)<>''));

CREATE FUNCTION delivery_document_sendable(c bigint,k text,d bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT EXISTS(
 SELECT 1 FROM estimates o JOIN customers p ON p.id=o.customer_id AND p.company_id=o.company_id
 WHERE k='estimate' AND o.id=d AND o.company_id=c AND o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND p.deleted_at IS NULL
 UNION ALL
 SELECT 1 FROM invoices o JOIN customers p ON p.id=o.customer_id AND p.company_id=o.company_id LEFT JOIN statuses s ON s.id=o.status_id
 WHERE k='invoice' AND o.id=d AND o.company_id=c AND o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND p.deleted_at IS NULL AND coalesce(s.category_key,'')<>'invoice:void')
$$;

CREATE OR REPLACE FUNCTION guard_document_delivery_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.company_id IS DISTINCT FROM NEW.company_id OR OLD.document_type IS DISTINCT FROM NEW.document_type OR OLD.document_id IS DISTINCT FROM NEW.document_id OR
 OLD.estimate_id IS DISTINCT FROM NEW.estimate_id OR OLD.invoice_id IS DISTINCT FROM NEW.invoice_id OR OLD.actor_id IS DISTINCT FROM NEW.actor_id OR
 OLD.idempotency_key IS DISTINCT FROM NEW.idempotency_key OR OLD.request_fingerprint IS DISTINCT FROM NEW.request_fingerprint OR
 OLD.recipients_to IS DISTINCT FROM NEW.recipients_to OR OLD.recipients_cc IS DISTINCT FROM NEW.recipients_cc OR OLD.recipients_bcc IS DISTINCT FROM NEW.recipients_bcc OR
 OLD.subject IS DISTINCT FROM NEW.subject OR OLD.text_body IS DISTINCT FROM NEW.text_body OR OLD.html_body IS DISTINCT FROM NEW.html_body OR
 OLD.pdf_data IS DISTINCT FROM NEW.pdf_data OR OLD.pdf_filename IS DISTINCT FROM NEW.pdf_filename OR OLD.message_id IS DISTINCT FROM NEW.message_id OR
 OLD.expected_status_id IS DISTINCT FROM NEW.expected_status_id OR OLD.tracking_enabled IS DISTINCT FROM NEW.tracking_enabled OR OLD.tracking_token_hash IS DISTINCT FROM NEW.tracking_token_hash OR OLD.created_at IS DISTINCT FROM NEW.created_at
 THEN RAISE EXCEPTION 'document delivery snapshot is immutable'; END IF;
 IF NEW.state IS DISTINCT FROM OLD.state THEN
   IF current_setting('freefsm.delivery_transition',true) IS DISTINCT FROM 'allowed' THEN RAISE EXCEPTION 'delivery state transition requires delivery module'; END IF;
   IF NOT ((OLD.state='queued' AND NEW.state IN ('sending','failed','cancelled')) OR (OLD.state='sending' AND NEW.state IN ('queued','accepted','failed','cancelled')) OR
     (OLD.state='accepted' AND NEW.state IN ('delivered','bounced')) OR (OLD.state='delivered' AND NEW.state='bounced') OR (OLD.state IN ('failed','bounced') AND NEW.state IN ('queued','cancelled')))
   THEN RAISE EXCEPTION 'invalid document delivery state transition % -> %',OLD.state,NEW.state; END IF;
 END IF;
 RETURN NEW;
END $$;

-- A failed/expired transport must never revive sendable retries after archive.
-- Run before the ordinary transition guard; the caller still needs transition
-- authority. Cancellation records its own truthful event in this transaction.
CREATE FUNCTION guard_delivery_readmission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (NEW.state IN ('queued','sending') OR (NEW.state='failed' AND OLD.state='sending')) AND
    (NEW.state IN ('queued','failed') OR (NEW.dispatch_boundary_known IS TRUE AND NEW.handoff_started_at IS NULL)) AND
    NOT delivery_document_sendable(NEW.company_id,NEW.document_type,NEW.document_id) THEN
  NEW.state='cancelled'; NEW.cancelled_at=clock_timestamp();
  NEW.last_error='Original document or Customer is archived, hidden, or Void';
  NEW.attempt_token=NULL; NEW.lease_expires_at=NULL;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER a_delivery_readmission BEFORE UPDATE ON document_deliveries FOR EACH ROW EXECUTE FUNCTION guard_delivery_readmission();

CREATE FUNCTION record_delivery_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.state='cancelled' AND OLD.state<>'cancelled' THEN
  INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,from_state,to_state,evidence)
  VALUES(NEW.id,NEW.company_id,'state',OLD.state,'cancelled',jsonb_build_object('reason',NEW.last_error,'dispatch_admitted',CASE WHEN OLD.dispatch_boundary_known IS TRUE THEN OLD.handoff_started_at IS NOT NULL ELSE NULL END));
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER delivery_cancellation_event AFTER UPDATE ON document_deliveries FOR EACH ROW EXECUTE FUNCTION record_delivery_cancellation();

CREATE FUNCTION cancel_unhanded_document_mail() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE previous text;
BEGIN
 previous:=current_setting('freefsm.delivery_transition',true);
 PERFORM set_config('freefsm.delivery_transition','allowed',true);
 UPDATE document_deliveries d SET state='cancelled',cancelled_at=clock_timestamp(),
  last_error='Original document or Customer is archived, hidden, or Void',attempt_token=NULL,lease_expires_at=NULL,updated_at=clock_timestamp()
 WHERE d.company_id=NEW.company_id AND (d.state IN ('queued','failed','bounced') OR (d.state='sending' AND d.dispatch_boundary_known IS TRUE AND d.handoff_started_at IS NULL))
 AND NOT delivery_document_sendable(d.company_id,d.document_type,d.document_id)
 AND ((TG_TABLE_NAME='customers' AND (
  EXISTS(SELECT 1 FROM estimates e WHERE e.id=d.estimate_id AND e.customer_id=NEW.id) OR
  EXISTS(SELECT 1 FROM invoices i WHERE i.id=d.invoice_id AND i.customer_id=NEW.id))) OR
  (TG_TABLE_NAME='estimates' AND d.estimate_id=NEW.id) OR (TG_TABLE_NAME='invoices' AND d.invoice_id=NEW.id));
 PERFORM set_config('freefsm.delivery_transition',coalesce(previous,''),true);
 RETURN NEW;
END $$;
CREATE TRIGGER cancel_estimate_mail AFTER UPDATE OF deleted_at,conversion_hidden_at ON estimates FOR EACH ROW EXECUTE FUNCTION cancel_unhanded_document_mail();
CREATE TRIGGER cancel_invoice_mail AFTER UPDATE OF deleted_at,conversion_hidden_at,status_id ON invoices FOR EACH ROW EXECUTE FUNCTION cancel_unhanded_document_mail();
CREATE TRIGGER cancel_customer_mail AFTER UPDATE OF deleted_at ON customers FOR EACH ROW EXECUTE FUNCTION cancel_unhanded_document_mail();

-- Also fence established/legacy SQL queue writers against Customer archive.
CREATE OR REPLACE FUNCTION validate_document_delivery_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE active boolean; status_type text;
BEGIN
 IF NEW.document_type='estimate' THEN
  SELECT o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND c.deleted_at IS NULL INTO active
  FROM estimates o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id
  WHERE o.id=NEW.estimate_id AND o.company_id=NEW.company_id FOR SHARE OF o,c;
 ELSE
  SELECT o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND c.deleted_at IS NULL AND coalesce(s.category_key,'')<>'invoice:void' INTO active
  FROM invoices o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id LEFT JOIN statuses s ON s.id=o.status_id
  WHERE o.id=NEW.invoice_id AND o.company_id=NEW.company_id FOR SHARE OF o,c;
 END IF;
 IF active IS DISTINCT FROM true THEN RAISE EXCEPTION 'delivery original document and Customer must be active'; END IF;
 IF NEW.expected_status_id IS NOT NULL THEN
  SELECT w.object_type INTO status_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id AND w.company_id=s.company_id WHERE s.id=NEW.expected_status_id AND s.company_id=NEW.company_id;
  IF status_type IS DISTINCT FROM NEW.document_type THEN RAISE EXCEPTION 'expected status must belong to document workflow'; END IF;
 END IF;
 RETURN NEW;
END $$;

-- Record a new, current cancellation for installed pending work whose original
-- source is already unavailable. Otherwise restoring that source before the
-- first worker sweep could resurrect pre-upgrade mail. This is not backdated
-- archive/transport evidence: immutable payload, identity and attempts remain,
-- and historical dispatch knowledge stays NULL. In-flight unknown work is left
-- for truthful lease recovery, not asserted to have been unhanded.
DO $$ DECLARE previous text;
BEGIN
 previous:=current_setting('freefsm.delivery_transition',true);
 PERFORM set_config('freefsm.delivery_transition','allowed',true);
 UPDATE document_deliveries SET state='cancelled',cancelled_at=clock_timestamp(),updated_at=clock_timestamp(),
  last_error='Original document or Customer unavailable when dispatch fencing enabled'
 WHERE state IN ('queued','failed','bounced') AND NOT delivery_document_sendable(company_id,document_type,document_id);
 PERFORM set_config('freefsm.delivery_transition',coalesce(previous,''),true);
END $$;
