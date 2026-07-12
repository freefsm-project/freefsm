ALTER TABLE company_settings ADD COLUMN email_tracking_enabled BOOLEAN NOT NULL DEFAULT false;
CREATE TABLE document_deliveries (
  id BIGSERIAL PRIMARY KEY,
  company_id BIGINT NOT NULL REFERENCES companies(id),
  document_type TEXT NOT NULL CHECK(document_type IN ('estimate','invoice')),
  document_id BIGINT NOT NULL,
  estimate_id BIGINT,
  invoice_id BIGINT,
  actor_id BIGINT NOT NULL,
  idempotency_key UUID NOT NULL,
  request_fingerprint BYTEA NOT NULL CHECK(octet_length(request_fingerprint)=32),
  recipients_to TEXT[] NOT NULL CHECK(cardinality(recipients_to)>0),
  recipients_cc TEXT[] NOT NULL DEFAULT '{}', recipients_bcc TEXT[] NOT NULL DEFAULT '{}',
  subject TEXT NOT NULL, text_body TEXT NOT NULL, html_body TEXT NOT NULL DEFAULT '',
  pdf_data BYTEA NOT NULL CHECK(octet_length(pdf_data)>0), pdf_filename TEXT NOT NULL CHECK(btrim(pdf_filename)<>''),
  message_id TEXT NOT NULL CHECK(btrim(message_id)<>''), expected_status_id BIGINT,
  state TEXT NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','sending','accepted','delivered','bounced','failed')),
  lifetime_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(lifetime_attempt_count>=0),
  cycle_attempt_count INTEGER NOT NULL DEFAULT 0 CHECK(cycle_attempt_count>=0), retry_cycle INTEGER NOT NULL DEFAULT 0 CHECK(retry_cycle>=0),
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(), attempt_token UUID, lease_expires_at TIMESTAMPTZ,
  provider_identifier TEXT NOT NULL DEFAULT '', acceptance_evidence JSONB NOT NULL DEFAULT '{}', last_error TEXT NOT NULL DEFAULT '',
  tracking_enabled BOOLEAN NOT NULL DEFAULT false, tracking_token_hash BYTEA,
  first_open_at TIMESTAMPTZ, last_open_at TIMESTAMPTZ, open_count INTEGER NOT NULL DEFAULT 0 CHECK(open_count>=0),
  accepted_at TIMESTAMPTZ, delivered_at TIMESTAMPTZ, bounced_at TIMESTAMPTZ, failed_at TIMESTAMPTZ,
	last_manual_retry_key UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE(id,company_id), UNIQUE(company_id,idempotency_key), UNIQUE(company_id,message_id),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id),
  FOREIGN KEY(estimate_id,company_id) REFERENCES estimates(id,company_id),
  FOREIGN KEY(invoice_id,company_id) REFERENCES invoices(id,company_id),
  FOREIGN KEY(expected_status_id,company_id) REFERENCES statuses(id,company_id),
  CHECK((document_type='estimate' AND estimate_id=document_id AND invoice_id IS NULL) OR (document_type='invoice' AND invoice_id=document_id AND estimate_id IS NULL)),
  CHECK((tracking_token_hash IS NOT NULL)=tracking_enabled AND (tracking_token_hash IS NULL OR octet_length(tracking_token_hash)=32)),
  CHECK((state='sending')=(attempt_token IS NOT NULL)), CHECK((state='sending')=(lease_expires_at IS NOT NULL)),
  CHECK(first_open_at IS NULL OR (last_open_at IS NOT NULL AND open_count>0)), CHECK(last_open_at IS NULL OR first_open_at<=last_open_at),
  CHECK(state NOT IN ('accepted','delivered','bounced') OR accepted_at IS NOT NULL),
  CHECK(state<>'delivered' OR delivered_at IS NOT NULL), CHECK(state<>'bounced' OR bounced_at IS NOT NULL), CHECK(state<>'failed' OR failed_at IS NOT NULL)
);
CREATE INDEX document_deliveries_claim_idx ON document_deliveries(next_attempt_at,id) WHERE state='queued';
CREATE INDEX document_deliveries_history_idx ON document_deliveries(company_id,document_type,document_id,created_at DESC);
CREATE UNIQUE INDEX document_deliveries_tracking_hash_idx ON document_deliveries(tracking_token_hash) WHERE tracking_token_hash IS NOT NULL;

CREATE TABLE document_delivery_events (
  id BIGSERIAL PRIMARY KEY, delivery_id BIGINT NOT NULL, company_id BIGINT NOT NULL,
  actor_id BIGINT, event_kind TEXT NOT NULL, from_state TEXT, to_state TEXT,
  provider_identifier TEXT NOT NULL DEFAULT '', provider_event_id TEXT, evidence JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY(delivery_id,company_id) REFERENCES document_deliveries(id,company_id),
  FOREIGN KEY(actor_id,company_id) REFERENCES users(id,company_id),
  CHECK((event_kind='state' AND to_state IS NOT NULL) OR (event_kind<>'state' AND from_state IS NULL AND to_state IS NULL))
);
CREATE UNIQUE INDEX document_delivery_provider_event_unique ON document_delivery_events(company_id,provider_identifier,provider_event_id) WHERE provider_event_id IS NOT NULL;
CREATE INDEX document_delivery_events_timeline_idx ON document_delivery_events(delivery_id,created_at,id);

CREATE FUNCTION validate_document_delivery_reference() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE status_type text;
BEGIN
 IF NEW.document_type='estimate' AND NOT EXISTS(SELECT 1 FROM estimates WHERE id=NEW.estimate_id AND company_id=NEW.company_id AND deleted_at IS NULL AND conversion_hidden_at IS NULL) THEN RAISE EXCEPTION 'delivery estimate must be active and company-owned'; END IF;
 IF NEW.document_type='invoice' AND NOT EXISTS(SELECT 1 FROM invoices WHERE id=NEW.invoice_id AND company_id=NEW.company_id AND deleted_at IS NULL AND conversion_hidden_at IS NULL) THEN RAISE EXCEPTION 'delivery invoice must be active and company-owned'; END IF;
 IF NEW.expected_status_id IS NOT NULL THEN
   SELECT w.object_type INTO status_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id AND w.company_id=s.company_id WHERE s.id=NEW.expected_status_id AND s.company_id=NEW.company_id;
   IF status_type IS DISTINCT FROM NEW.document_type THEN RAISE EXCEPTION 'expected status must belong to document workflow'; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER document_delivery_reference_guard BEFORE INSERT ON document_deliveries FOR EACH ROW EXECUTE FUNCTION validate_document_delivery_reference();

CREATE FUNCTION guard_document_delivery_update() RETURNS trigger LANGUAGE plpgsql AS $$
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
   IF NOT ((OLD.state='queued' AND NEW.state IN ('sending','failed')) OR (OLD.state='sending' AND NEW.state IN ('queued','accepted','failed')) OR
     (OLD.state='accepted' AND NEW.state IN ('delivered','bounced')) OR (OLD.state='delivered' AND NEW.state='bounced') OR (OLD.state IN ('failed','bounced') AND NEW.state='queued'))
   THEN RAISE EXCEPTION 'invalid document delivery state transition % -> %',OLD.state,NEW.state; END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER document_delivery_update_guard BEFORE UPDATE ON document_deliveries FOR EACH ROW EXECUTE FUNCTION guard_document_delivery_update();
CREATE FUNCTION guard_document_delivery_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'document deliveries are immutable history'; END $$;
CREATE TRIGGER document_delivery_no_delete BEFORE DELETE ON document_deliveries FOR EACH ROW EXECUTE FUNCTION guard_document_delivery_delete();
CREATE FUNCTION guard_document_delivery_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'document delivery events are append-only'; END $$;
CREATE TRIGGER document_delivery_event_immutable BEFORE UPDATE OR DELETE ON document_delivery_events FOR EACH ROW EXECUTE FUNCTION guard_document_delivery_event();
